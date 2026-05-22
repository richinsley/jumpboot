package jumpboot

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The Julia test environment is created once per run and cached at a fixed
// path under the OS temp directory so repeated runs reuse the micromamba env.
var (
	testJuliaEnvOnce sync.Once
	testJuliaEnv     *JuliaEnvironment
	testJuliaEnvErr  error
)

// getTestJuliaEnv creates (or reuses) a micromamba-managed Julia environment.
// Tests that need Julia are skipped (not failed) if it cannot be created.
func getTestJuliaEnv(t *testing.T) *JuliaEnvironment {
	t.Helper()
	testJuliaEnvOnce.Do(func() {
		root := filepath.Join(os.TempDir(), "jumpboot-julia-testenv")
		if err := os.MkdirAll(root, 0755); err != nil {
			testJuliaEnvErr = err
			return
		}
		testJuliaEnv, testJuliaEnvErr = CreateJuliaEnvironmentMamba("juliaenv", root, "", "conda-forge", nil)
	})
	if testJuliaEnvErr != nil {
		t.Skipf("could not create Julia test environment: %v", testJuliaEnvErr)
	}
	return testJuliaEnv
}

// juliaQueueTestPlugin exercises the QueueProcess RPC surface from Julia: a
// plain call, a crash, and a streaming method.
const juliaQueueTestPlugin = `
register("echo", (data, ctx) -> data["text"])
register("crash_now", (data, ctx) -> exit(1))
register("emit_n", function (data, ctx)
    n = get(data, "n", 5)
    for i in 0:(n - 1)
        emit(ctx, Dict("chunk" => i))
    end
    return Dict("total" => n)
end)
serve()
`

func getJuliaQueueProcess(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestJuliaEnv(t)

	program := &JuliaProgram{
		Name:         "julia_queue_test",
		PluginSource: juliaQueueTestPlugin,
	}

	queue, err := env.NewQueueProcess(program, nil)
	if err != nil {
		t.Fatalf("Failed to create Julia QueueProcess: %v", err)
	}
	return queue
}

func TestCreateJuliaEnvironmentMamba(t *testing.T) {
	env := getTestJuliaEnv(t)

	if env.JuliaVersion.Major < 1 {
		t.Errorf("expected a valid Julia version, got %q", env.JuliaVersion.String())
	}
	if _, err := os.Stat(env.JuliaPath); err != nil {
		t.Errorf("julia executable not found at %s: %v", env.JuliaPath, err)
	}
}

func TestJuliaQueueProcess_BasicCall(t *testing.T) {
	queue := getJuliaQueueProcess(t)
	defer queue.Close()

	result, err := queue.Call("echo", 10, map[string]interface{}{"text": "hello"})
	if err != nil {
		t.Fatalf("echo call failed: %v", err)
	}
	if got, ok := result.(string); !ok || got != "hello" {
		t.Fatalf("expected 'hello', got %T %v", result, result)
	}
}

func TestJuliaQueueProcess_AliveAfterCreation(t *testing.T) {
	queue := getJuliaQueueProcess(t)
	defer queue.Close()

	if !queue.Alive() {
		t.Error("Julia process should be alive after creation")
	}
}

func TestJuliaQueueProcess_NotAliveAfterClose(t *testing.T) {
	queue := getJuliaQueueProcess(t)

	queue.Close()

	select {
	case <-queue.ExitChan():
	case <-time.After(10 * time.Second):
		t.Fatal("Julia process did not exit after Close")
	}
	if queue.Alive() {
		t.Error("Julia process should not be alive after Close")
	}
}

func TestJuliaQueueProcess_CrashViaExit(t *testing.T) {
	queue := getJuliaQueueProcess(t)
	defer queue.Close()

	_, err := queue.Call("crash_now", 10, nil)
	if err == nil {
		t.Fatal("crash_now should return an error")
	}
	t.Logf("got expected error: %v", err)

	select {
	case <-queue.ExitChan():
	case <-time.After(5 * time.Second):
		t.Fatal("Julia process did not exit after exit(1)")
	}
	if queue.Alive() {
		t.Error("Julia process should not be alive after exit(1)")
	}
}

// TestJuliaCallStream_MultiFrame: emit_n streams 5 partials + 1 terminal frame.
func TestJuliaCallStream_MultiFrame(t *testing.T) {
	queue := getJuliaQueueProcess(t)
	defer queue.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
