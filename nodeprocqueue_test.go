package jumpboot

import (
	"context"
	"testing"
	"time"
)

// nodeQueueTestScript is a Node.js service exercising the QueueProcess RPC
// surface: a plain call, a crash, a streaming method, and a cooperatively
// cancellable streaming method. It is the Node.js analog of queueTestScript /
// streamTestScript / cancelTestScript on the Python side.
const nodeQueueTestScript = `
const { MessagePackQueueServer } = require('jumpboot');

class TestService extends MessagePackQueueServer {
    async echo(data) {
        return data.text;
    }

    async crash_now(data) {
        process.exit(1);
    }

    async emit_n(data, requestId, ctx) {
        const n = (data && data.n) || 5;
        for (let i = 0; i < n; i++) {
            ctx.emit({ chunk: i });
        }
        return { total: n };
    }

    async emit_n_cancellable(data, requestId, ctx) {
        const n = (data && data.n) || 100;
        let emitted = 0;
        for (let i = 0; i < n; i++) {
            if (ctx.cancelled()) {
                return { cancelled: true, emitted: emitted };
            }
            ctx.emit({ chunk: i });
            emitted++;
            await new Promise((r) => setTimeout(r, 50));
        }
        return { total: n };
    }
}

new TestService();
`

func getNodeQueueProcess(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestNodeEnv(t)

	program := &NodeProgram{
		Name:    "node_queue_test",
		Path:    ".",
		Program: *NewModuleFromString("__main__", "test_queue.js", nodeQueueTestScript),
	}

	queue, err := env.NewQueueProcess(program, nil, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create Node QueueProcess: %v", err)
	}
	return queue
}

func TestNodeQueueProcess_BasicCall(t *testing.T) {
	queue := getNodeQueueProcess(t)
	defer queue.Close()

	result, err := queue.Call("echo", 5, map[string]interface{}{"text": "hello"})
	if err != nil {
		t.Fatalf("echo call failed: %v", err)
	}
	if got, ok := result.(string); !ok || got != "hello" {
		t.Fatalf("expected 'hello', got %T %v", result, result)
	}
}

func TestNodeQueueProcess_AliveAfterCreation(t *testing.T) {
	queue := getNodeQueueProcess(t)
	defer queue.Close()

	if !queue.Alive() {
		t.Error("Node process should be alive after creation")
	}
}

func TestNodeQueueProcess_NotAliveAfterClose(t *testing.T) {
	queue := getNodeQueueProcess(t)

	queue.Close()

	select {
	case <-queue.ExitChan():
	case <-time.After(10 * time.Second):
		t.Fatal("Node process did not exit after Close")
	}
	if queue.Alive() {
		t.Error("Node process should not be alive after Close")
	}
}

func TestNodeQueueProcess_CrashViaProcessExit(t *testing.T) {
	queue := getNodeQueueProcess(t)
	defer queue.Close()

	// crash_now calls process.exit(1) before replying; the call must fail.
	_, err := queue.Call("crash_now", 5, nil)
	if err == nil {
		t.Fatal("crash_now should return an error")
	}
	t.Logf("got expected error: %v", err)

	select {
	case <-queue.ExitChan():
	case <-time.After(5 * time.Second):
		t.Fatal("Node process did not exit after process.exit(1)")
	}
	if queue.Alive() {
		t.Error("Node process should not be alive after process.exit(1)")
	}
}

// TestNodeCallStream_MultiFrame: emit_n streams 5 partials + 1 terminal frame.
func TestNodeCallStream_MultiFrame(t *testing.T) {
	queue := getNodeQueueProcess(t)
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
		t.Fatalf("expected 6 frames (5 partials + 1 terminal), got %d: %+v", len(frames), frames)
	}
	for i := 0; i < 5; i++ {
		chunk, ok := frames[i]["chunk"]
		if !ok {
			t.Errorf("frame %d missing 'chunk': %+v", i, frames[i])
			continue
		}
		iv, ok := conformanceInt(chunk)
		if !ok {
			t.Errorf("frame %d 'chunk' not numeric: %T", i, chunk)
			continue
		}
		if iv != int64(i) {
			t.Errorf("frame %d expected chunk=%d, got %d", i, i, iv)
		}
		if d, _ := frames[i]["done"].(bool); d {
			t.Errorf("frame %d expected done=false", i)
		}
	}
	if d, _ := frames[5]["done"].(bool); !d {
		t.Errorf("terminal frame should have done=true, got %+v", frames[5])
	}
}

// TestNodeCallStream_CancelMidStream: cancel after a few frames; the service's
// ctx.cancelled() poll must observe it and the channel must close promptly.
func TestNodeCallStream_CancelMidStream(t *testing.T) {
	queue := getNodeQueueProcess(t)
	defer queue.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := queue.CallStream(ctx, "emit_n_cancellable", map[string]interface{}{"n": 100})
	if err != nil {
		t.Fatalf("CallStream failed: %v", err)
	}

	received := 0
	t0 := time.Now()
	for range ch {
		received++
		if received == 3 {
			cancel()
		}
	}
	elapsed := time.Since(t0)

	if received < 3 {
		t.Fatalf("expected at least 3 frames before cancel, got %d", received)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("stream took too long to close after cancel: %v", elapsed)
	}
	t.Logf("received %d frames before stream closed, elapsed=%v", received, elapsed)
}
