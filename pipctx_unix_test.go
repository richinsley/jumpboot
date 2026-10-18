//go:build linux || darwin

package jumpboot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ctxPip writes a fake pip running body, in a directory that also receives
// the PID files it writes, and returns the environment and the directory.
func ctxPip(t *testing.T, body string) (*PythonEnvironment, string) {
	t.Helper()
	dir := t.TempDir()
	pip := filepath.Join(dir, "pip")
	if err := os.WriteFile(pip, []byte("#!/bin/sh\ncd "+dir+"\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &PythonEnvironment{PipPath: pip}, dir
}

// readPID reads a PID written to dir/name.
func readPID(t *testing.T, dir, name string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// gone reports whether no process pid exists any more, waiting up to two
// seconds (an orphan's zombie exists until init reaps it). A process still
// there is killed, so that a failing test leaves nothing behind.
func gone(t *testing.T, pid int) bool {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return false
}

func TestPipInstallContextSucceeds(t *testing.T) {
	env, _ := ctxPip(t, "printf 'a\\nb\\nc\\n'\n")
	var calls []int64
	err := pipInstallContext(context.Background(), env, []string{"x"}, "", "", true, func(msg string, current, total int64) {
		calls = append(calls, current)
	})
	if err != nil || len(calls) != 4 || calls[2] != 3 || calls[3] != 100 {
		t.Fatalf("calls %v, err %v", calls, err)
	}
	env, _ = ctxPip(t, "echo 'fatal pip detail' >&2\nexit 7\n")
	if err := pipInstallContext(context.Background(), env, []string{"x"}, "", "", true, nil); err == nil ||
		!strings.Contains(err.Error(), "fatal pip detail") || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("failure: %v", err)
	}
}

// TestPipInstallContextCancelKillsGroup cancels while pip, and a child it
// started (as pip starts build backends), are running: both are gone.
func TestPipInstallContextCancelKillsGroup(t *testing.T) {
	env, dir := ctxPip(t, "echo $$ > pip.pid\nsleep 60 &\necho $! > child.pid\necho started\nwait\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	err := pipInstallContext(ctx, env, []string{"x"}, "", "", true, func(msg string, current, total int64) {
		if current == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("cancellation took %v", d)
	}
	for _, name := range []string{"pip.pid", "child.pid"} {
		if pid := readPID(t, dir, name); !gone(t, pid) {
			t.Errorf("%s (%d) still runs after the cancellation", name, pid)
		}
	}
}

func TestPipInstallContextDeadline(t *testing.T) {
	env, dir := ctxPip(t, "echo $$ > pip.pid\nsleep 60\n")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := pipInstallContext(ctx, env, []string{"x"}, "", "", true, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the 300ms deadline took %v", d)
	}
	if pid := readPID(t, dir, "pip.pid"); !gone(t, pid) {
		t.Errorf("pip (%d) still runs after the deadline", pid)
	}
}

func TestPipInstallContextPanickingCallback(t *testing.T) {
	// More output than a pipe holds, and a long run: pip must be stopped
	// and reaped, not waited out.
	env, dir := ctxPip(t, "echo $$ > pip.pid\ni=0; while [ $i -lt 100000 ]; do echo line $i padding padding padding; i=$((i+1)); done\nsleep 60\n")
	var recovered any
	start := time.Now()
	func() {
		defer func() { recovered = recover() }()
		_ = pipInstallContext(context.Background(), env, []string{"x"}, "", "", true, func(string, int64, int64) { panic("callback") })
	}()
	if recovered != "callback" {
		t.Fatalf("recovered %v, want the callback's panic", recovered)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the panic took %v to reach the caller", d)
	}
	// Reaped by pipInstallContext: not even a zombie is left.
	if pid := readPID(t, dir, "pip.pid"); !gone(t, pid) {
		t.Errorf("pip (%d) was not reaped", pid)
	}
}

// TestPipInstallContextEscapedOutput cancels while a process outside pip's
// group keeps pip's standard output open: the call still returns.
func TestPipInstallContextEscapedOutput(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid")
	}
	env, dir := ctxPip(t, "setsid sh -c 'echo $$ > escaped.pid; exec sleep 60' &\n"+
		"while [ ! -s escaped.pid ]; do sleep 0.05; done\necho started\nwait\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	err := pipInstallContext(ctx, env, []string{"x"}, "", "", true, func(msg string, current, total int64) {
		if current == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 3*pipWaitDelay {
		t.Errorf("returned after %v", d)
	}
	// The escaped process outlived the group kill, holding the output: end
	// it here.
	escaped := readPID(t, dir, "escaped.pid")
	if err := syscall.Kill(escaped, 0); err != nil {
		t.Errorf("the escaped process (%d) did not escape: %v", escaped, err)
	}
	_ = syscall.Kill(escaped, syscall.SIGKILL)
}

// TestRestorePipPackagesHonoursContext checks that the restore's pip step
// passes its context to pip: a deadline stops a pip that hangs.
func TestRestorePipPackagesHonoursContext(t *testing.T) {
	env, dir := ctxPip(t, "echo $$ > pip.pid\nsleep 60\n")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	for _, es := range []*EnvironmentSpec{
		{Packages: []PackageSpec{{Name: "requests", Version: "2.31.0", Source: "pip"}}},
		{PipPackages: []string{"requests==2.31.0"}},
	} {
		if err := restorePipPackages(ctx, env, es, nil); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want context.DeadlineExceeded", err)
		}
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v", d)
	}
	if pid := readPID(t, dir, "pip.pid"); !gone(t, pid) {
		t.Errorf("pip (%d) still runs", pid)
	}
}
