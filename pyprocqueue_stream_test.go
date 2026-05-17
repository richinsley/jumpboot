package jumpboot

import (
	"context"
	"testing"
	"time"
)

// streamTestScript is a service that exercises the raw streaming protocol at
// the jumpboot layer. The handlers call ``self.send_response`` directly with
// partial frames (done=False) followed by a terminal frame (done=True) so we
// can verify multi-frame delivery without depending on jb-service's
// CallContext wrapper (which lives a layer above).
const streamTestScript = `
import asyncio
from jumpboot import MessagePackQueueServer

class StreamTestService(MessagePackQueueServer):
    def __init__(self):
        super().__init__(auto_start=False, expose_methods=False)
        self.register_handler("emit_n", self._emit_n)
        self.register_handler("emit_n_cancellable", self._emit_n_cancellable)
        self.register_handler("plain_call", self._plain_call)
        self.start()

    async def _emit_n(self, data, request_id):
        # Verify the dispatcher saw the stream flag.
        if not self.is_streaming(request_id):
            return {"error": "expected stream=True for emit_n"}
        n = (data or {}).get("n", 5)
        for i in range(n):
            self.send_response({"chunk": i, "done": False}, request_id)
            await asyncio.sleep(0)  # let event loop process other tasks
        # _process_command will publish this as the terminal frame.
        return {"total": n, "done": True}

    async def _emit_n_cancellable(self, data, request_id):
        n = (data or {}).get("n", 10)
        event = self.get_cancel_event(request_id)
        emitted = 0
        for i in range(n):
            if event is not None and event.is_set():
                return {"cancelled": True, "emitted": emitted, "done": True}
            self.send_response({"chunk": i, "done": False}, request_id)
            emitted += 1
            await asyncio.sleep(0.05)
        return {"total": n, "done": True}

    async def _plain_call(self, data, request_id):
        # Non-streaming method: returns a single value with no done field.
        # Callers using CallStream should still get exactly one frame.
        return {"value": (data or {}).get("v", "default")}

import time
service = StreamTestService()
while service.running:
    time.sleep(0.5)
`

func getStreamTestQueue(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestEnv(t)
	program := &PythonProgram{
		Name:    "stream_test",
		Path:    ".",
		Program: *NewModuleFromString("__main__", "test_stream.py", streamTestScript),
	}
	queue, err := env.NewQueueProcess(program, nil, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create QueueProcess: %v", err)
	}
	return queue
}

// TestCallStream_MultiFrame: emits 5 partials + 1 final. Verify in-order
// delivery and that the channel closes after the terminal frame.
func TestCallStream_MultiFrame(t *testing.T) {
	queue := getStreamTestQueue(t)
	defer queue.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch, err := queue.CallStream(ctx, "emit_n", map[string]interface{}{"n": 5})
	if err != nil {
		t.Fatalf("CallStream failed: %v", err)
	}

	var frames []map[string]interface{}
	for f := range ch {
		frames = append(frames, f)
	}

	if len(frames) != 6 {
		t.Fatalf("expected 6 frames (5 partials + 1 final), got %d: %+v", len(frames), frames)
	}
	// First 5 are partials with consecutive chunk values.
	for i := 0; i < 5; i++ {
		chunk, ok := frames[i]["chunk"]
		if !ok {
			t.Errorf("frame %d missing 'chunk' field: %+v", i, frames[i])
			continue
		}
		// msgpack decodes integers as int8/uint8/etc — coerce.
		var iv int
		switch v := chunk.(type) {
		case int8:
			iv = int(v)
		case uint8:
			iv = int(v)
		case int64:
			iv = int(v)
		case uint64:
			iv = int(v)
		case int:
			iv = v
		default:
			t.Errorf("frame %d chunk has unexpected type %T", i, chunk)
			continue
		}
		if iv != i {
			t.Errorf("frame %d expected chunk=%d, got %d", i, i, iv)
		}
		if d, _ := frames[i]["done"].(bool); d {
			t.Errorf("frame %d expected done=false, got true", i)
		}
	}
	// Final frame has done:true and total field.
	if d, _ := frames[5]["done"].(bool); !d {
		t.Errorf("final frame should have done=true, got %+v", frames[5])
	}
}

// TestCallStream_PlainCall: a non-streaming method called via CallStream
// should still yield exactly one frame (the legacy single-reply form has no
// done field, treated as terminal).
func TestCallStream_PlainCall(t *testing.T) {
	queue := getStreamTestQueue(t)
	defer queue.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := queue.CallStream(ctx, "plain_call", map[string]interface{}{"v": "hi"})
	if err != nil {
		t.Fatalf("CallStream failed: %v", err)
	}

	count := 0
	for range ch {
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 frame from non-streaming method, got %d", count)
	}
}

// TestCallStream_CancelMidStream: cancel ctx after a few frames; verify the
// service observes the cancel and the channel closes promptly.
func TestCallStream_CancelMidStream(t *testing.T) {
	queue := getStreamTestQueue(t)
	defer queue.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := queue.CallStream(ctx, "emit_n_cancellable", map[string]interface{}{"n": 100})
	if err != nil {
		t.Fatalf("CallStream failed: %v", err)
	}

	// Receive a few frames, then cancel.
	received := 0
	t0 := time.Now()
	for f := range ch {
		received++
		if received == 3 {
			cancel()
		}
		_ = f
	}
	elapsed := time.Since(t0)

	if received < 3 {
		t.Fatalf("expected at least 3 frames before cancel, got %d", received)
	}
	// The channel should close within ~1s of the cancel (cancel event +
	// next-iteration poll on Python side + frame in transit).
	if elapsed > 3*time.Second {
		t.Fatalf("stream took too long to close after cancel: %v", elapsed)
	}
	t.Logf("received %d frames before stream closed, elapsed=%v", received, elapsed)
}

// TestCallStream_AlreadyDoneFailsFast: ctx already done — no request is sent.
func TestCallStream_AlreadyDoneFailsFast(t *testing.T) {
	queue := getStreamTestQueue(t)
	defer queue.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := queue.CallStream(ctx, "emit_n", nil)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
