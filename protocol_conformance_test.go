package jumpboot

import (
	"strings"
	"testing"
)

// This file holds the cross-language protocol conformance harness. The wire
// protocol is specified in docs/PROTOCOL.md; runProtocolConformance asserts the
// behaviours every runtime SDK must satisfy when driven by an unmodified
// QueueProcess.
//
// The harness is deliberately runtime-agnostic: it takes an already-constructed
// *QueueProcess and never assumes which language is on the other end. Each
// runtime gets its own Test* entry point that builds a queue and calls the
// harness, so every SDK is held to the identical bar. Today only Python is
// wired up (TestProtocolConformance_Python); the Node.js entry point lands with
// Phase 1.

// conformanceService is the reference service every runtime SDK must expose for
// the conformance harness. The Node.js SDK will ship a behaviourally identical
// service so the same assertions run against it unchanged.
const conformanceService = `
import time
from jumpboot import MessagePackQueueServer, exposed

class ConformanceService(MessagePackQueueServer):
    @exposed
    async def echo(self, text: str = "") -> str:
        """Echo the given text straight back."""
        return text

    @exposed
    async def add(self, a: int = 0, b: int = 0) -> int:
        """Return the sum of two integers."""
        return a + b

    @exposed
    async def boom(self) -> None:
        """Always raise, to exercise error propagation."""
        raise ValueError("intentional conformance failure")

service = ConformanceService()
while service.running:
    time.sleep(1)
`

// conformanceInt coerces a MessagePack-decoded numeric result to int64.
// MessagePack decodes small integers into the narrowest type, so a queue Call
// result may arrive as any of the int/uint widths.
func conformanceInt(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	default:
		return 0, false
	}
}

// runProtocolConformance runs the language-neutral protocol assertions against
// a queue whose runtime exposes the conformanceService methods.
func runProtocolConformance(t *testing.T, queue *QueueProcess) {
	t.Helper()

	t.Run("BasicCall", func(t *testing.T) {
		result, err := queue.Call("echo", 5, map[string]interface{}{"text": "hello"})
		if err != nil {
			t.Fatalf("echo call failed: %v", err)
		}
		got, ok := result.(string)
		if !ok {
			t.Fatalf("expected string result, got %T: %v", result, result)
		}
		if got != "hello" {
			t.Errorf("expected echoed 'hello', got %q", got)
		}
	})

	t.Run("MultiArgCall", func(t *testing.T) {
		result, err := queue.Call("add", 5, map[string]interface{}{"a": 2, "b": 3})
		if err != nil {
			t.Fatalf("add call failed: %v", err)
		}
		got, ok := conformanceInt(result)
		if !ok {
			t.Fatalf("expected numeric result, got %T: %v", result, result)
		}
		if got != 5 {
			t.Errorf("expected 2 + 3 == 5, got %d", got)
		}
	})

	t.Run("ErrorPropagation", func(t *testing.T) {
		_, err := queue.Call("boom", 5, nil)
		if err == nil {
			t.Fatal("boom call should have returned an error")
		}
		if !strings.Contains(err.Error(), "intentional conformance failure") {
			t.Errorf("error should carry the Python message, got: %v", err)
		}
	})

	t.Run("MethodDiscovery", func(t *testing.T) {
		methods := queue.GetMethods()
		want := map[string]bool{"echo": false, "add": false, "boom": false}
		for _, name := range methods {
			if _, expected := want[name]; expected {
				want[name] = true
			}
		}
		for name, found := range want {
			if !found {
				t.Errorf("discovered methods missing %q (got %v)", name, methods)
			}
		}
	})
}

// getConformanceQueue builds a QueueProcess backed by a Python conformance
// service. The Node.js equivalent lands with Phase 1.
func getConformanceQueue(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestEnv(t)

	program := &PythonProgram{
		Name:    "conformance",
		Path:    ".",
		Program: *NewModuleFromString("__main__", "conformance.py", conformanceService),
	}

	queue, err := env.NewQueueProcess(program, nil, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conformance QueueProcess: %v", err)
	}
	return queue
}

func TestProtocolConformance_Python(t *testing.T) {
	queue := getConformanceQueue(t)
	defer queue.Close()
	runProtocolConformance(t, queue)
}

// conformanceServiceJS is the Node.js reference service. It is behaviourally
// identical to conformanceService (Python) so the same harness assertions run
// against it unchanged.
const conformanceServiceJS = `
const { MessagePackQueueServer } = require('jumpboot');

class ConformanceService extends MessagePackQueueServer {
    async echo(data) {
        return data.text;
    }
    async add(data) {
        return data.a + data.b;
    }
    async boom(data) {
        throw new Error('intentional conformance failure');
    }
}

new ConformanceService();
`

// getConformanceNodeQueue builds a QueueProcess backed by a Node.js conformance
// service.
func getConformanceNodeQueue(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestNodeEnv(t)

	program := &NodeProgram{
		Name:    "conformance",
		Path:    ".",
		Program: *NewModuleFromString("__main__", "conformance.js", conformanceServiceJS),
	}

	queue, err := env.NewQueueProcess(program, nil, nil, nil)
	if err != nil {
		t.Fatalf("failed to create Node conformance QueueProcess: %v", err)
	}
	return queue
}

func TestProtocolConformance_Node(t *testing.T) {
	queue := getConformanceNodeQueue(t)
	defer queue.Close()
	runProtocolConformance(t, queue)
}

// conformanceServiceWasm is the WebAssembly reference plugin — a Go program
// compiled to wasip1/wasm and run in-process by wazero. It is behaviourally
// identical to the Python and Node.js conformance services so the same
// harness assertions run against it unchanged.
const conformanceServiceWasm = `
package main

import (
	"errors"

	"github.com/richinsley/jumpboot/wasmguest"
)

func main() {
	wasmguest.Register("echo", func(data any, ctx *wasmguest.Context) (any, error) {
		m, _ := data.(map[string]any)
		return m["text"], nil
	})
	wasmguest.Register("add", func(data any, ctx *wasmguest.Context) (any, error) {
		m, _ := data.(map[string]any)
		return wasmInt(m["a"]) + wasmInt(m["b"]), nil
	})
	wasmguest.Register("boom", func(data any, ctx *wasmguest.Context) (any, error) {
		return nil, errors.New("intentional conformance failure")
	})
	wasmguest.Serve()
}

func wasmInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}
`

// getConformanceWasmQueue builds a QueueProcess backed by a WebAssembly
// conformance plugin compiled and run in-process via wazero.
func getConformanceWasmQueue(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestWasmEnv(t)

	program := &WasmProgram{
		Name:         "conformance",
		PluginSource: conformanceServiceWasm,
	}

	queue, err := env.NewQueueProcess(program, nil)
	if err != nil {
		t.Fatalf("failed to create WASM conformance QueueProcess: %v", err)
	}
	return queue
}

func TestProtocolConformance_Wasm(t *testing.T) {
	queue := getConformanceWasmQueue(t)
	defer queue.Close()
	runProtocolConformance(t, queue)
}

// conformanceServiceDeno is the Deno reference service. The embedded Deno SDK
// is in scope, so the plugin subclasses MessagePackQueueServer directly.
const conformanceServiceDeno = `
class ConformanceService extends MessagePackQueueServer {
    async echo(data) {
        return data.text;
    }
    async add(data) {
        return data.a + data.b;
    }
    async boom(data) {
        throw new Error('intentional conformance failure');
    }
}

new ConformanceService();
`

// getConformanceDenoQueue builds a QueueProcess backed by a Deno conformance
// service.
func getConformanceDenoQueue(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestDenoEnv(t)

	program := &DenoProgram{
		Name:         "conformance",
		PluginSource: conformanceServiceDeno,
	}

	queue, err := env.NewQueueProcess(program, nil)
	if err != nil {
		t.Fatalf("failed to create Deno conformance QueueProcess: %v", err)
	}
	return queue
}

func TestProtocolConformance_Deno(t *testing.T) {
	queue := getConformanceDenoQueue(t)
	defer queue.Close()
	runProtocolConformance(t, queue)
}

// conformanceServiceJulia is the Julia reference service. The embedded Julia
// SDK is in scope, so the plugin calls register/serve directly.
const conformanceServiceJulia = `
register("echo", (data, ctx) -> data["text"])
register("add", (data, ctx) -> data["a"] + data["b"])
register("boom", (data, ctx) -> error("intentional conformance failure"))
serve()
`

// getConformanceJuliaQueue builds a QueueProcess backed by a Julia conformance
// service.
func getConformanceJuliaQueue(t *testing.T) *QueueProcess {
	t.Helper()
	env := getTestJuliaEnv(t)

	program := &JuliaProgram{
		Name:         "conformance",
		PluginSource: conformanceServiceJulia,
	}

	queue, err := env.NewQueueProcess(program, nil)
	if err != nil {
		t.Fatalf("failed to create Julia conformance QueueProcess: %v", err)
	}
	return queue
}

func TestProtocolConformance_Julia(t *testing.T) {
	queue := getConformanceJuliaQueue(t)
	defer queue.Close()
	runProtocolConformance(t, queue)
}
