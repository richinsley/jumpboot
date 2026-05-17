package jumpboot

import (
	"context"
	"testing"
	"time"
)

// cancelTestScript implements the MessagePackQueueServer protocol for testing
// the cooperative cancellation path. _slow_cancellable polls the asyncio.Event
// returned by self.get_cancel_event(request_id) — the same primitive that
// jb-service's CallContext will wrap once Phase 1 lands.
const cancelTestScript = `
import asyncio
import time
from jumpboot import MessagePackQueueServer

class CancelTestService(MessagePackQueueServer):
    def __init__(self):
        super().__init__(auto_start=False, expose_methods=False)
        self.register_handler("slow_cancellable", self._slow_cancellable)
        self.register_handler("echo", self._echo)
        self.start()

    async def _echo(self, data, request_id):
        text = (data or {}).get("text", "")
        return {"echoed": text}

    async def _slow_cancellable(self, data, request_id):
        """Loop with cooperative cancel polling.
        Returns {"cancelled": True, "iterations": N} if cancelled.
        Returns {"completed": True, "iterations": N} otherwise.
        Each iteration is ~0.1s; delay arg is the maximum seconds to run.
        """
        delay = (data or {}).get("delay", 60)
        event = self.get_cancel_event(request_id)
        iterations = int(delay * 10)
        for i in range(iterations):
            if event is not None and event.is_set():
                return {"cancelled": True, "iterations": i}
            await asyncio.sleep(0.1)
        return {"completed": True, "iterations": iterations}

service = CancelTestService()
while service.running:
    time.sleep(0.5)
`

func getCancelTestQueue(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestEnv(t)

	program := &PythonProgram{
		Name:    "cancel_test",
		Path:    ".",
		Program: *NewModuleFromString("__main__", "test_cancel.py", cancelTestScript),
	}

	queue, err := env.NewQueueProcess(program, nil, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create QueueProcess: %v", err)
	}
	return queue
}

// TestCallContext_Success: ctx not cancelled — CallContext behaves like Call.
func TestCallContext_Success(t *testing.T) {
	queue := getCancelTestQueue(t)
	defer queue.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := queue.CallContext(ctx, "echo", map[string]interface{}{"text": "hi"})
	if err != nil {
		t.Fatalf("CallContext failed: %v", err)
	}
	// Result may come back as either a map or a bare string — the existing Call()
	// code (and our extractCallResult) unwraps single-key dicts.
	switch v := result.(type) {
	case map[string]interface{}:
		if v["echoed"] != "hi" {
			t.Errorf("expected echoed='hi', got %v", v["echoed"])
		}
	case string:
		if v != "hi" {
			t.Errorf("expected 'hi', got %q", v)
		}
	default:
		t.Fatalf("unexpected result type %T: %v", result, result)
	}
}

// TestCallContext_AlreadyDoneFailsFast: ctx already cancelled — no command is sent.
func TestCallContext_AlreadyDoneFailsFast(t *testing.T) {
	queue := getCancelTestQueue(t)
	defer queue.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // fire immediately

	_, err := queue.CallContext(ctx, "echo", map[string]interface{}{"text": "hi"})
	if err == nil {
		t.Fatal("CallContext should return error when ctx already done")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestCallContext_CancelMidCall: kick off a slow call, cancel ctx, verify the
// Python side observed it and the call returns context.Canceled promptly.
func TestCallContext_CancelMidCall(t *testing.T) {
	queue := getCancelTestQueue(t)
	defer queue.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type res struct {
		val any
		err error
	}
	resCh := make(chan res, 1)
	go func() {
		v, e := queue.CallContext(ctx, "slow_cancellable", map[string]interface{}{"delay": 30})
		resCh <- res{v, e}
	}()

	// Give the Python side a moment to enter the loop and register its cancel event.
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case r := <-resCh:
		if r.err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v (val=%v)", r.err, r.val)
		}
		// We expect the Python side to have actually observed the cancel within
		// the grace window. The call should return within ~5s of ctx firing.
	case <-time.After(8 * time.Second):
		t.Fatal("CallContext did not return within 8s of cancellation")
	}
}

// TestSendCancel_UnknownID: cancelling a non-existent request_id is fire-and-forget
// — the ack message says "no in-flight call" but is silently dropped by Go since
// we don't register a response channel for the cancel command itself.
func TestSendCancel_UnknownID(t *testing.T) {
	queue := getCancelTestQueue(t)
	defer queue.Close()

	if err := queue.SendCancel("req-nonexistent"); err != nil {
		t.Fatalf("SendCancel for unknown id should not error: %v", err)
	}
	// Follow-up call should still work — the cancel didn't disturb the message loop.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := queue.CallContext(ctx, "echo", map[string]interface{}{"text": "ok"}); err != nil {
		t.Fatalf("subsequent CallContext failed: %v", err)
	}
}
