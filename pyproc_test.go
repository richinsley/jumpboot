package jumpboot

import (
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"
)

// getTestEnv returns a system Python environment for testing.
// Skips the test if no Python is available.
func getTestEnv(t *testing.T) *PythonEnvironment {
	t.Helper()
	env, err := CreateEnvironmentFromSystem()
	if err != nil {
		t.Skipf("No system Python available: %v", err)
	}
	return env
}

// startTestProgram starts a Python process via NewPythonProcessFromProgram.
// This is more reliable than NewPythonProcessFromString for testing because
// the bootstrap mechanism fully sets up __name__ == "__main__".
func startTestProgram(t *testing.T, env *PythonEnvironment, script string) *PythonProcess {
	t.Helper()
	program := &PythonProgram{
		Name: "test",
		Path: ".",
		Program: Module{
			Name:   "__main__",
			Path:   "test.py",
			Source: base64.StdEncoding.EncodeToString([]byte(script)),
		},
	}
	proc, _, err := env.NewPythonProcessFromProgram(program, nil, nil, false)
	if err != nil {
		t.Fatalf("Failed to start Python process: %v", err)
	}
	return proc
}

// longRunningScript is a Python script that stays alive until killed.
const longRunningScript = `
import time
while True:
    time.sleep(1)
`

func TestAlive_RunningProcess(t *testing.T) {
	env := getTestEnv(t)
	proc := startTestProgram(t, env, longRunningScript)
	proc.MonitorProcess()
	defer proc.Terminate()

	// Give bootstrap a moment to finish
	time.Sleep(500 * time.Millisecond)

	if !proc.Alive() {
		t.Errorf("Process should be alive; exitErr=%v", proc.ExitError())
	}
}

func TestAlive_AfterExit(t *testing.T) {
	env := getTestEnv(t)
	proc := startTestProgram(t, env, `import sys; sys.exit(0)`)
	proc.MonitorProcess()

	select {
	case <-proc.ExitChan():
	case <-time.After(5 * time.Second):
		t.Fatal("Process did not exit within 5s")
	}

	if proc.Alive() {
		t.Error("Process should not be alive after exit")
	}
}

func TestExitError_NonZeroExit(t *testing.T) {
	env := getTestEnv(t)
	proc := startTestProgram(t, env, `import sys; sys.exit(42)`)
	proc.MonitorProcess()

	<-proc.ExitChan()

	if err := proc.ExitError(); err == nil {
		t.Error("Non-zero exit should produce an error")
	}
}

func TestExitChan_BlocksWhileRunning(t *testing.T) {
	env := getTestEnv(t)
	proc := startTestProgram(t, env, longRunningScript)
	proc.MonitorProcess()
	defer proc.Terminate()

	// Wait for bootstrap to complete
	time.Sleep(500 * time.Millisecond)

	select {
	case <-proc.ExitChan():
		t.Errorf("ExitChan should not be closed while process is running; exitErr=%v", proc.ExitError())
	case <-time.After(500 * time.Millisecond):
		// Expected — channel still open
	}
}

func TestExitChan_ClosedAfterExit(t *testing.T) {
	env := getTestEnv(t)
	proc := startTestProgram(t, env, `import sys; sys.exit(0)`)
	proc.MonitorProcess()

	select {
	case <-proc.ExitChan():
		// Expected
	case <-time.After(5 * time.Second):
		t.Fatal("ExitChan should be closed after process exits")
	}
}

func TestExitChan_NilWithoutMonitor(t *testing.T) {
	env := getTestEnv(t)
	proc := startTestProgram(t, env, longRunningScript)
	defer proc.Terminate()

	if proc.ExitChan() != nil {
		t.Error("ExitChan should be nil before MonitorProcess is called")
	}
}

func TestOnExit_CalledOnExit(t *testing.T) {
	env := getTestEnv(t)
	proc := startTestProgram(t, env, `import sys; sys.exit(0)`)

	var called bool
	var mu sync.Mutex
	proc.OnExit = func(err error) {
		mu.Lock()
		called = true
		mu.Unlock()
	}
	proc.MonitorProcess()

	<-proc.ExitChan()
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Error("OnExit callback should have been called")
	}
}

func TestOnExit_ReceivesErrorOnCrash(t *testing.T) {
	env := getTestEnv(t)
	proc := startTestProgram(t, env, `import sys; sys.exit(1)`)

	var exitErr error
	var mu sync.Mutex
	proc.OnExit = func(err error) {
		mu.Lock()
		exitErr = err
		mu.Unlock()
	}
	proc.MonitorProcess()

	<-proc.ExitChan()
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if exitErr == nil {
		t.Error("OnExit should receive non-nil error for non-zero exit")
	}
}

func TestMonitorProcess_Idempotent(t *testing.T) {
	env := getTestEnv(t)
	proc := startTestProgram(t, env, longRunningScript)
	defer proc.Terminate()

	// Call multiple times — should not panic or start multiple goroutines
	proc.MonitorProcess()
	proc.MonitorProcess()
	proc.MonitorProcess()

	time.Sleep(500 * time.Millisecond)
	if !proc.Alive() {
		t.Errorf("Process should still be alive; exitErr=%v", proc.ExitError())
	}
}

func TestTerminate_RunningProcess(t *testing.T) {
	env := getTestEnv(t)
	proc := startTestProgram(t, env, longRunningScript)
	proc.MonitorProcess()

	time.Sleep(500 * time.Millisecond)

	proc.Terminate()

	if proc.Alive() {
		t.Error("Process should not be alive after Terminate")
	}
}

func TestErrProcessExited_Sentinel(t *testing.T) {
	wrapped := errors.Join(ErrProcessExited, errors.New("exit status 1"))
	if !errors.Is(wrapped, ErrProcessExited) {
		t.Error("errors.Is should match ErrProcessExited in wrapped error")
	}
}
