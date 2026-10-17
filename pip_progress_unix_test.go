//go:build !windows

package jumpboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPipScript(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pip")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPipInstallPackagesStreamsProgressUntilExit(t *testing.T) {
	ack := filepath.Join(t.TempDir(), "first-progress-seen")
	t.Setenv("JUMPBOOT_PIP_PROGRESS_ACK", ack)
	pip := testPipScript(t, `
printf 'first line\n'
count=0
while [ ! -f "$JUMPBOOT_PIP_PROGRESS_ACK" ]; do
  count=$((count + 1))
  if [ "$count" -ge 40 ]; then
    printf 'first progress callback never arrived\n' >&2
    exit 9
  fi
  sleep 0.05
done
sleep 0.05
printf 'second line\n'
`)
	env := &PythonEnvironment{PipPath: pip}
	var descriptions []string
	var current []int64
	err := env.PipInstallPackages([]string{"demo"}, "", "", false, func(message string, completed, total int64) {
		descriptions = append(descriptions, message)
		current = append(current, completed)
		if completed == 1 {
			if writeErr := os.WriteFile(ack, []byte("ok"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 3 || current[0] != 1 || current[1] != 2 || current[2] != 100 {
		t.Fatalf("progress counts = %v, want [1 2 100]", current)
	}
	if descriptions[0] != "Installing pip package demo..." || descriptions[1] != descriptions[0] || descriptions[2] != "Pip packages installed successfully" {
		t.Fatalf("progress descriptions = %v", descriptions)
	}
}

func TestPipInstallPackagesRetainsStderrOnFailure(t *testing.T) {
	pip := testPipScript(t, "printf 'fatal pip detail\\n' >&2\nexit 7\n")
	env := &PythonEnvironment{PipPath: pip}
	var callbacks int
	err := env.PipInstallPackages([]string{"demo"}, "", "", false, func(string, int64, int64) {
		callbacks++
	})
	if err == nil || !strings.Contains(err.Error(), "fatal pip detail") || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("failure = %v, want exit status and stderr detail", err)
	}
	if callbacks != 0 {
		t.Fatalf("success callback ran on failure: %d callbacks", callbacks)
	}
}
