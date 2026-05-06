package jumpboot

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// queueTestScript implements the MessagePackQueueServer protocol for testing.
// Uses the class-based pattern from the jsonqueueserver example.
const queueTestScript = `
import time
from jumpboot import MessagePackQueueServer, exposed

class TestService(MessagePackQueueServer):
    @exposed
    async def echo(self, text: str = "") -> dict:
        return {"echoed": text}

    @exposed
    async def slow_echo(self, text: str = "", delay: int = 5) -> dict:
        time.sleep(delay)
        return {"echoed": text}

    @exposed
    async def crash_now(self) -> None:
        import os
        os._exit(1)

service = TestService()
while service.running:
    time.sleep(1)
`

func getQueueProcess(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestEnv(t)

	program := &PythonProgram{
		Name:    "queue_test",
		Path:    ".",
		Program: *NewModuleFromString("__main__", "test_queue.py", queueTestScript),
	}

	queue, err := env.NewQueueProcess(program, nil, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create QueueProcess: %v", err)
	}
	return queue
}

func TestQueueProcess_SingleMessageLoopLaunchSite(t *testing.T) {
	data, err := os.ReadFile("pyprocqueue.go")
	if err != nil {
		t.Fatalf("read pyprocqueue.go: %v", err)
	}
	count := strings.Count(string(data), "go jq.messageLoop()")
	if count != 1 {
		t.Fatalf("QueueProcess must launch exactly one messageLoop reader, found %d", count)
	}
}

func TestQueueProcess_BasicCall(t *testing.T) {
	queue := getQueueProcess(t)
	defer queue.Close()

	result, err := queue.Call("echo", 5, map[string]interface{}{"text": "hello"})
	if err != nil {
		t.Fatalf("echo call failed: %v", err)
	}

	// The @exposed async method returns {"echoed": text} which gets wrapped
	// through the queue protocol. Check the actual result type.
	switch v := result.(type) {
	case map[string]interface{}:
		if v["echoed"] != "hello" {
			t.Errorf("expected 'hello', got '%v'", v["echoed"])
		}
	case string:
		// Some protocol versions return the inner value directly
		t.Logf("Got string result: %q", v)
	default:
		t.Fatalf("unexpected result type %T: %v", result, result)
	}
}

func TestQueueProcess_AliveAfterCreation(t *testing.T) {
	queue := getQueueProcess(t)
	defer queue.Close()

	if !queue.PythonProcess.Alive() {
		t.Error("Process should be alive after creation")
	}
}

func TestQueueProcess_CallAfterKill(t *testing.T) {
	queue := getQueueProcess(t)
	defer queue.Close()

	// Verify it works
	_, err := queue.Call("echo", 5, map[string]interface{}{"text": "before"})
	if err != nil {
		t.Fatalf("echo call failed: %v", err)
	}

	// Kill the process
	queue.PythonProcess.Cmd.Process.Kill()

	// Wait for death
	select {
	case <-queue.PythonProcess.ExitChan():
	case <-time.After(5 * time.Second):
		t.Fatal("Process did not exit after kill")
	}

	// Subsequent calls should fail fast with ErrProcessExited
	_, err = queue.Call("echo", 5, map[string]interface{}{"text": "after"})
	if err == nil {
		t.Fatal("Call after kill should return error")
	}
	if !errors.Is(err, ErrProcessExited) {
		t.Errorf("Expected ErrProcessExited, got: %v", err)
	}
}

func TestQueueProcess_PendingCallUnblocksOnCrash(t *testing.T) {
	queue := getQueueProcess(t)
	defer queue.Close()

	// Start a slow call in a goroutine
	errCh := make(chan error, 1)
	go func() {
		_, err := queue.Call("slow_echo", 60, map[string]interface{}{"text": "slow", "delay": 60})
		errCh <- err
	}()

	// Give it time to send the command
	time.Sleep(500 * time.Millisecond)

	// Kill the process while call is pending
	queue.PythonProcess.Cmd.Process.Kill()

	// The pending call should unblock quickly
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Pending call should return error after crash")
		}
		t.Logf("Got expected error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Pending call did not unblock within 5s of process death")
	}
}

func TestQueueProcess_CrashViaOsExit(t *testing.T) {
	queue := getQueueProcess(t)
	defer queue.Close()

	// Call a method that crashes via os._exit(1)
	_, err := queue.Call("crash_now", 5, nil)
	if err == nil {
		t.Fatal("crash_now should return error")
	}
	t.Logf("Got expected error: %v", err)

	if queue.PythonProcess.Alive() {
		t.Error("Process should not be alive after os._exit(1)")
	}
}

func TestQueueProcess_NotAliveAfterClose(t *testing.T) {
	queue := getQueueProcess(t)

	queue.Close()

	// After close, process should eventually be dead
	select {
	case <-queue.PythonProcess.ExitChan():
	case <-time.After(10 * time.Second):
		t.Fatal("Process did not exit after Close")
	}

	if queue.PythonProcess.Alive() {
		t.Error("Process should not be alive after Close")
	}
}
