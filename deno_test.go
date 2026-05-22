package jumpboot

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The Deno test environment is created once per run and cached at a fixed path
// under the OS temp directory so repeated runs reuse the micromamba env.
var (
	testDenoEnvOnce sync.Once
	testDenoEnv     *DenoEnvironment
	testDenoEnvErr  error
)

// getTestDenoEnv creates (or reuses) a micromamba-managed Deno environment.
// Tests that need Deno are skipped (not failed) if it cannot be created.
func getTestDenoEnv(t *testing.T) *DenoEnvironment {
	t.Helper()
	testDenoEnvOnce.Do(func() {
		root := filepath.Join(os.TempDir(), "jumpboot-deno-testenv")
		if err := os.MkdirAll(root, 0755); err != nil {
			testDenoEnvErr = err
			return
		}
		testDenoEnv, testDenoEnvErr = CreateDenoEnvironmentMamba("denoenv", root, "", "conda-forge", nil)
	})
	if testDenoEnvErr != nil {
		t.Skipf("could not create Deno test environment: %v", testDenoEnvErr)
	}
	return testDenoEnv
}

// denoQueueTestPlugin exercises the QueueProcess RPC surface from Deno: a plain
// call, a crash, a streaming method, and a cooperatively cancellable one.
const denoQueueTestPlugin = `
class TestService extends MessagePackQueueServer {
    async echo(data) {
        return data.text;
    }
    async crash_now(data) {
        Deno.exit(1);
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

func getDenoQueueProcess(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestDenoEnv(t)

	program := &DenoProgram{
		Name:         "deno_queue_test",
		PluginSource: denoQueueTestPlugin,
	}

	queue, err := env.NewQueueProcess(program, nil)
	if err != nil {
		t.Fatalf("Failed to create Deno QueueProcess: %v", err)
	}
	return queue
}

func TestCreateDenoEnvironmentMamba(t *testing.T) {
	env := getTestDenoEnv(t)

	if env.DenoVersion.Major < 1 {
		t.Errorf("expected a valid Deno version, got %q", env.DenoVersion.String())
	}
	if _, err := os.Stat(env.DenoPath); err != nil {
		t.Errorf("deno executable not found at %s: %v", env.DenoPath, err)
	}
}

func TestDenoQueueProcess_BasicCall(t *testing.T) {
	queue := getDenoQueueProcess(t)
	defer queue.Close()

	result, err := queue.Call("echo", 5, map[string]interface{}{"text": "hello"})
	if err != nil {
		t.Fatalf("echo call failed: %v", err)
	}
	if got, ok := result.(string); !ok || got != "hello" {
		t.Fatalf("expected 'hello', got %T %v", result, result)
	}
}

func TestDenoQueueProcess_AliveAfterCreation(t *testing.T) {
	queue := getDenoQueueProcess(t)
	defer queue.Close()

	if !queue.Alive() {
		t.Error("Deno process should be alive after creation")
	}
}

func TestDenoQueueProcess_NotAliveAfterClose(t *testing.T) {
	queue := getDenoQueueProcess(t)

	queue.Close()

	select {
	case <-queue.ExitChan():
	case <-time.After(10 * time.Second):
		t.Fatal("Deno process did not exit after Close")
	}
	if queue.Alive() {
		t.Error("Deno process should not be alive after Close")
	}
}

func TestDenoQueueProcess_CrashViaExit(t *testing.T) {
	queue := getDenoQueueProcess(t)
	defer queue.Close()

	_, err := queue.Call("crash_now", 5, nil)
	if err == nil {
		t.Fatal("crash_now should return an error")
	}
	t.Logf("got expected error: %v", err)

	select {
	case <-queue.ExitChan():
	case <-time.After(5 * time.Second):
		t.Fatal("Deno process did not exit after Deno.exit(1)")
	}
	if queue.Alive() {
		t.Error("Deno process should not be alive after Deno.exit(1)")
	}
}

// TestDenoCallStream_MultiFrame: emit_n streams 5 partials + 1 terminal frame.
func TestDenoCallStream_MultiFrame(t *testing.T) {
	queue := getDenoQueueProcess(t)
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
		if !ok || iv != int64(i) {
			t.Errorf("frame %d expected chunk=%d, got %v", i, i, chunk)
		}
	}
	if d, _ := frames[5]["done"].(bool); !d {
		t.Errorf("terminal frame should have done=true, got %+v", frames[5])
	}
}

// TestDenoCallStream_CancelMidStream: cancel after a few frames; the service's
// ctx.cancelled() poll must observe it and the channel must close promptly.
func TestDenoCallStream_CancelMidStream(t *testing.T) {
	queue := getDenoQueueProcess(t)
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
