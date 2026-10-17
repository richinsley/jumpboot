//go:build !windows

package jumpboot

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestRunReadStdoutReapsChild(t *testing.T) {
	out, err := RunReadStdout("/bin/sh", "-c", "printf '%s\n' \"$$\"")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("parse child PID from %q: %v", out, err)
	}

	// A successful exec.Cmd.Wait has already reaped the direct child. If it
	// has not, Wait4 reaps it here so a failing test leaves no zombie behind.
	var status syscall.WaitStatus
	reaped, err := syscall.Wait4(pid, &status, 0, nil)
	if !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("child %d was left waitable: Wait4 returned pid=%d, err=%v", pid, reaped, err)
	}
}

func TestRunReadStdoutReportsExitStatus(t *testing.T) {
	_, err := RunReadStdout("/bin/sh", "-c", "exit 7")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("exit status = %v, want 7", err)
	}
}
