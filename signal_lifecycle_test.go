package jumpboot

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestSignalLifecycleChild(t *testing.T) {
	if os.Getenv("JUMPBOOT_SIGNAL_LIFECYCLE_HELPER") == "sleep" {
		time.Sleep(30 * time.Second)
	}
}

func signalLifecycleCommand(t *testing.T, sleep bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalLifecycleChild$")
	mode := "exit"
	if sleep {
		mode = "sleep"
	}
	cmd.Env = append(os.Environ(), "JUMPBOOT_SIGNAL_LIFECYCLE_HELPER="+mode)
	prepareProcessCommand(cmd)
	return cmd
}

func waitForNoSignalListenerLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+4 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("signal listeners remain after children exit: goroutines before=%d after=%d", before, runtime.NumGoroutine())
}

func TestPythonProcessSignalListenersStopAfterExit(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 16; i++ {
		cmd := signalLifecycleCommand(t, false)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		proc := &PythonProcess{Cmd: cmd}
		setupSignalHandler(proc)
		if i%2 == 0 {
			if err := proc.Wait(); err != nil {
				t.Fatal(err)
			}
		} else {
			proc.MonitorProcess()
			select {
			case <-proc.ExitChan():
			case <-time.After(5 * time.Second):
				t.Fatal("monitored child did not exit")
			}
			if err := proc.ExitError(); err != nil {
				t.Fatal(err)
			}
		}
	}
	waitForNoSignalListenerLeak(t, before)
}

func TestPythonProcessSignalListenersStopAfterTerminate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows SIGTERM semantics differ from Unix")
	}
	before := runtime.NumGoroutine()
	for i := 0; i < 12; i++ {
		cmd := signalLifecycleCommand(t, true)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		proc := &PythonProcess{Cmd: cmd}
		setupSignalHandler(proc)
		if err := proc.Terminate(); err != nil {
			t.Fatal(err)
		}
	}
	waitForNoSignalListenerLeak(t, before)
}

func TestPythonProcessSignalListenerStillTerminatesLiveChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows SIGTERM semantics differ from Unix")
	}
	cmd := signalLifecycleCommand(t, true)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	proc := &PythonProcess{Cmd: cmd}
	setupSignalHandler(proc)
	proc.MonitorProcess()
	defer proc.Terminate()
	proc.signalChan <- os.Interrupt
	select {
	case <-proc.ExitChan():
	case <-time.After(5 * time.Second):
		t.Fatal("signal listener did not terminate the child")
	}
	if proc.Alive() {
		t.Fatal("child still marked alive after signal")
	}
}

func TestBoundRunPythonScriptSignalListenersStopAfterExit(t *testing.T) {
	before := runtime.NumGoroutine()
	env := &PythonEnvironment{PythonPath: os.Args[0]}
	t.Setenv("JUMPBOOT_SIGNAL_LIFECYCLE_HELPER", "exit")
	for i := 0; i < 16; i++ {
		if err := env.BoundRunPythonScriptFromFile("-test.run=^TestSignalLifecycleChild$"); err != nil {
			t.Fatal(err)
		}
	}
	waitForNoSignalListenerLeak(t, before)
}
