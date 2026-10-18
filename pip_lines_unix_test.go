//go:build linux || darwin

package jumpboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePip writes a shell script standing in for pip and returns an
// environment that uses it.
func fakePip(t *testing.T, body string) *PythonEnvironment {
	t.Helper()
	pip := filepath.Join(t.TempDir(), "pip")
	if err := os.WriteFile(pip, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &PythonEnvironment{PipPath: pip}
}

// TestPipInstallPackagesProgressLines checks that progress is reported once
// per line of pip's standard output, also after a 200 KB line (beyond
// bufio.Scanner's default limit, within PipInstallPackages' 1 MB one), and
// while pip first fills its standard error beyond a pipe's capacity, and
// that pip's failure and standard error reach the error.
func TestPipInstallPackagesProgressLines(t *testing.T) {
	env := fakePip(t, ""+
		"head -c 2000000 /dev/zero | tr '\\0' e >&2\n"+ // more than a pipe holds
		"head -c 200000 /dev/zero | tr '\\0' o; echo\n"+ // longer than bufio.Scanner's default limit
		"i=0; while [ $i -lt 1000 ]; do echo line $i; i=$((i+1)); done\n"+
		"exit ${JUMPBOOT_TEST_PIP_EXIT:-0}\n")
	for _, code := range []string{"0", "1"} {
		t.Setenv("JUMPBOOT_TEST_PIP_EXIT", code)
		var calls, last int64
		done := make(chan error, 1)
		go func() {
			done <- env.PipInstallPackages([]string{"x"}, "", "", false, func(msg string, current, total int64) {
				if strings.HasPrefix(msg, "Installing pip package") {
					calls++
					last = current
				}
			})
		}()
		select {
		case err := <-done:
			if (code == "0") != (err == nil) || (err != nil && !strings.Contains(err.Error(), "stderr: eeee")) {
				t.Errorf("exit %s: err = %.80v", code, err)
			}
		case <-time.After(time.Minute):
			t.Fatalf("exit %s: pip never finished", code)
		}
		if calls != 1001 || last != 1001 {
			t.Errorf("exit %s: %d progress calls, last %d; want 1001", code, calls, last)
		}
	}
}
