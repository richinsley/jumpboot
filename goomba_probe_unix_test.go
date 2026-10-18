//go:build linux || darwin

package jumpboot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/richinsley/goomba"
	"github.com/richinsley/goomba/virtual"
)

// TestRunProbeContextCapsOutput checks that a version probe keeps at most
// probeOutputLimit bytes of a large output and still finishes.
func TestRunProbeContextCapsOutput(t *testing.T) {
	probe, _ := ctxPip(t, "head -c 1000000 /dev/zero | tr '\\0' x\necho\n")
	out, err := runProbeContext(context.Background(), probe.PipPath, "--version")
	if err != nil || len(out) != probeOutputLimit || strings.Trim(out, "x") != "" {
		t.Fatalf("output of %d bytes, err %v; want %d bytes", len(out), err, probeOutputLimit)
	}
}

// TestGoombaVersionCheckStops runs the python --version check against a
// fake python that hangs (and starts a sleeping child), in a new
// environment's transaction and on reuse. Timeout and cancellation stop
// it: the call returns promptly with the context's error, a new
// environment is rolled back and its lock released, and the fake python
// and its child are gone.
func TestGoombaVersionCheckStops(t *testing.T) {
	refRoot := os.Getenv("GOOMBA_REF_ROOT")
	if refRoot == "" {
		t.Skip("set GOOMBA_REF_ROOT to a micromamba root holding envs/base-3.11 and its pkgs/ cache")
	}
	ch := startRefChannel(t, refRoot, virtual.HostPlatform())
	root := t.TempDir()
	opts := GoombaOptions{PythonVersion: "3.11", Channels: []string{ch.url}, Client: ch.srv.Client()}
	// The environment to reuse, which also warms the package cache, so
	// that the Timeouts below are spent in the check.
	if _, err := CreateEnvironmentGoombaWithOptions(context.Background(), "warm", root, opts, nil); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		env     string // the environment to create or reuse
		timeout time.Duration
		cancel  bool
		want    error
	}{
		{"create timeout", "hang-timeout", 6 * time.Second, false, context.DeadlineExceeded},
		{"create cancelled", "hang-cancelled", 0, true, context.Canceled},
		{"reuse timeout", "warm", 2 * time.Second, false, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			python, dir := ctxPip(t, "echo $$ > probe.pid\nsleep 60 &\necho $! > child.pid\nwait\n")
			orig := probeExecutable
			probeExecutable = func(path string) string {
				if filepath.Base(path) == "python" {
					return python.PipPath
				}
				return path
			}
			defer func() { probeExecutable = orig }()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o := opts
			o.Timeout = tc.timeout
			start := time.Now()
			_, err := CreateEnvironmentGoombaWithOptions(ctx, tc.env, root, o, func(msg string, current, total int64) {
				if tc.cancel && msg == "Checking the environment" {
					// Cancel once the hung probe runs.
					go func() {
						for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
							if _, err := os.Stat(filepath.Join(dir, "child.pid")); err == nil {
								break
							}
						}
						cancel()
					}()
				}
			})
			elapsed := time.Since(start)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if !tc.cancel && !strings.Contains(fmtErr(err), "timed out after "+tc.timeout.String()) {
				t.Errorf("err = %v, want the Timeout named", err)
			}
			if elapsed > tc.timeout+10*time.Second {
				t.Errorf("returned after %v: the check did not stop", elapsed)
			}
			prefix := filepath.Join(root, "envs", tc.env)
			status := goomba.CheckComplete(prefix)
			if errors.Is(status, goomba.ErrPrefixLocked) {
				t.Errorf("the environment is still locked: %v", status)
			}
			if tc.env == "warm" {
				if status != nil {
					t.Errorf("the reused environment is no longer a finished one: %v", status)
				}
			} else if _, err := os.Lstat(prefix); !os.IsNotExist(err) {
				t.Errorf("the environment was left (%v)", err)
			}
			for _, f := range []string{"probe.pid", "child.pid"} {
				if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
					t.Fatalf("the fake python never ran (%v)", err)
				}
				if pid := readPID(t, dir, f); !gone(t, pid) {
					t.Errorf("%s (%d) still runs", f, pid)
				}
			}
		})
	}
}

// fmtErr returns err's message, or "" for nil.
func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
