package jumpboot

import (
	"strings"
	"testing"
)

func getNodeREPL(t *testing.T) *NodeREPLProcess {
	t.Helper()
	env := getTestNodeEnv(t)
	repl, err := env.NewREPLProcess(nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("failed to create Node REPL: %v", err)
	}
	return repl
}

func TestNodeREPL_BasicExecute(t *testing.T) {
	repl := getNodeREPL(t)
	defer repl.Close()

	out, err := repl.Execute("1 + 1", true)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if out != "2" {
		t.Errorf("expected '2', got %q", out)
	}
}

func TestNodeREPL_StatePersists(t *testing.T) {
	repl := getNodeREPL(t)
	defer repl.Close()

	if _, err := repl.Execute("var x = 42", true); err != nil {
		t.Fatalf("first Execute failed: %v", err)
	}
	out, err := repl.Execute("x * 2", true)
	if err != nil {
		t.Fatalf("second Execute failed: %v", err)
	}
	if out != "84" {
		t.Errorf("expected persisted state to yield '84', got %q", out)
	}
}

func TestNodeREPL_ConsoleOutput(t *testing.T) {
	repl := getNodeREPL(t)
	defer repl.Close()

	out, err := repl.Execute("console.log('hello from node')", true)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if out != "hello from node" {
		t.Errorf("expected captured console output, got %q", out)
	}
}

func TestNodeREPL_Exception(t *testing.T) {
	repl := getNodeREPL(t)
	defer repl.Close()

	out, err := repl.Execute("throw new Error('boom')", true)
	if err == nil {
		t.Fatal("expected an error from a throwing statement")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should mention 'boom', got: %v", err)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("output should contain the stack trace mentioning 'boom', got: %q", out)
	}

	// The REPL must remain usable after an exception.
	out, err = repl.Execute("3 + 4", true)
	if err != nil || out != "7" {
		t.Errorf("REPL should recover after an exception; got out=%q err=%v", out, err)
	}
}

func TestNodeREPL_CombinedOutputToggle(t *testing.T) {
	repl := getNodeREPL(t)
	defer repl.Close()

	// With combinedOutput=true, console.error is captured.
	out, err := repl.Execute("console.error('an error line')", true)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if out != "an error line" {
		t.Errorf("expected console.error captured in combined mode, got %q", out)
	}

	// With combinedOutput=false, console.error is not captured.
	out, err = repl.Execute("console.error('hidden')", false)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if out != "" {
		t.Errorf("expected console.error not captured in non-combined mode, got %q", out)
	}
}

func TestNodeREPL_RequireWorks(t *testing.T) {
	repl := getNodeREPL(t)
	defer repl.Close()

	out, err := repl.Execute("require('os').EOL.length", true)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if out != "1" {
		t.Errorf("expected require('os') to work, got %q", out)
	}
}

func TestNodeREPL_NotUsableAfterClose(t *testing.T) {
	repl := getNodeREPL(t)

	if err := repl.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if _, err := repl.Execute("1 + 1", true); err == nil {
		t.Error("Execute should fail after Close")
	}
}
