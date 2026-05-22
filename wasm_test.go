package jumpboot

import (
	"context"
	"sync"
	"testing"
	"time"
)

// The WASM test environment uses the system Go toolchain. It is created once
// per run and cached.
var (
	testWasmEnvOnce sync.Once
	testWasmEnv     *WasmEnvironment
	testWasmEnvErr  error
)

// getTestWasmEnv returns a WasmEnvironment backed by the system Go toolchain.
// Tests that need WASM are skipped (not failed) if no usable Go is found.
func getTestWasmEnv(t *testing.T) *WasmEnvironment {
	t.Helper()
	testWasmEnvOnce.Do(func() {
		testWasmEnv, testWasmEnvErr = CreateWasmEnvironment()
	})
	if testWasmEnvErr != nil {
		t.Skipf("could not create WASM environment: %v", testWasmEnvErr)
	}
	return testWasmEnv
}

// wasmQueueTestPlugin exercises the QueueProcess RPC surface from a WASM
// plugin: a plain call, a crash via os.Exit, and a streaming method.
const wasmQueueTestPlugin = `
package main

import (
	"os"

	"github.com/richinsley/jumpboot/wasmguest"
)

func main() {
	wasmguest.Register("echo", func(data any, ctx *wasmguest.Context) (any, error) {
		m, _ := data.(map[string]any)
		return m["text"], nil
	})
	wasmguest.Register("crash_now", func(data any, ctx *wasmguest.Context) (any, error) {
		os.Exit(1)
		return nil, nil
	})
	wasmguest.Register("emit_n", func(data any, ctx *wasmguest.Context) (any, error) {
		m, _ := data.(map[string]any)
		n := int64(5)
		if v, ok := m["n"].(int64); ok {
			n = v
		}
		for i := int64(0); i < n; i++ {
			ctx.Emit(map[string]any{"chunk": i})
		}
		return map[string]any{"total": n}, nil
	})
	wasmguest.Serve()
}
`

func getWasmQueueProcess(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestWasmEnv(t)

	program := &WasmProgram{
		Name:         "wasm_queue_test",
		PluginSource: wasmQueueTestPlugin,
	}

	queue, err := env.NewQueueProcess(program, nil)
	if err != nil {
		t.Fatalf("Failed to create WASM QueueProcess: %v", err)
	}
	return queue
}

func TestCreateWasmEnvironment(t *testing.T) {
	env := getTestWasmEnv(t)
	if env.GoPath == "" {
		t.Error("expected a non-empty Go toolchain path")
	}
}

func TestWasmQueueProcess_BasicCall(t *testing.T) {
	queue := getWasmQueueProcess(t)
	defer queue.Close()

	result, err := queue.Call("echo", 5, map[string]interface{}{"text": "hello"})
	if err != nil {
		t.Fatalf("echo call failed: %v", err)
	}
	if got, ok := result.(string); !ok || got != "hello" {
		t.Fatalf("expected 'hello', got %T %v", result, result)
	}
}

func TestWasmQueueProcess_AliveAfterCreation(t *testing.T) {
	queue := getWasmQueueProcess(t)
	defer queue.Close()

	if !queue.Alive() {
		t.Error("WASM module should be alive after creation")
	}
}

func TestWasmQueueProcess_NotAliveAfterClose(t *testing.T) {
	queue := getWasmQueueProcess(t)

	queue.Close()

	select {
	case <-queue.ExitChan():
	case <-time.After(10 * time.Second):
		t.Fatal("WASM module did not exit after Close")
	}
	if queue.Alive() {
		t.Error("WASM module should not be alive after Close")
	}
}

func TestWasmQueueProcess_CrashViaOsExit(t *testing.T) {
	queue := getWasmQueueProcess(t)
	defer queue.Close()

	_, err := queue.Call("crash_now", 5, nil)
	if err == nil {
		t.Fatal("crash_now should return an error")
	}
	t.Logf("got expected error: %v", err)

	select {
	case <-queue.ExitChan():
	case <-time.After(5 * time.Second):
		t.Fatal("WASM module did not exit after os.Exit(1)")
	}
	if queue.Alive() {
		t.Error("WASM module should not be alive after os.Exit(1)")
	}
}

// TestWasmCallStream_MultiFrame: emit_n streams 5 partials + 1 terminal frame.
func TestWasmCallStream_MultiFrame(t *testing.T) {
	queue := getWasmQueueProcess(t)
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
		if d, _ := frames[i]["done"].(bool); d {
			t.Errorf("frame %d expected done=false", i)
		}
	}
	if d, _ := frames[5]["done"].(bool); !d {
		t.Errorf("terminal frame should have done=true, got %+v", frames[5])
	}
}
