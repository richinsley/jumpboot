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

	"github.com/richinsley/goomba/virtual"
)

// TestGoombaRestorePipStepStops runs a restore whose pip step hangs (a fake
// pip that prints a line, starts a sleeping child and sleeps), and stops it
// once by cancelling ctx from the pip progress callback and once with
// GoombaOptions.Timeout. Either way the call returns promptly with the
// context's error, nothing is left, and pip and its child are gone: the
// restore's pip step really runs under the hook's context.
func TestGoombaRestorePipStepStops(t *testing.T) {
	refRoot := os.Getenv("GOOMBA_REF_ROOT")
	if refRoot == "" {
		t.Skip("set GOOMBA_REF_ROOT to a micromamba root holding envs/base-3.11 and its pkgs/ cache")
	}
	ch := startRefChannel(t, refRoot, virtual.HostPlatform())
	var python map[string]any
	for _, sd := range ch.index {
		for _, e := range sd {
			if e["name"] == "python" && strings.HasPrefix(e["version"].(string), "3.11.") {
				python = e
			}
		}
	}
	if python == nil {
		t.Skip("no python 3.11 in $GOOMBA_REF_ROOT/pkgs")
	}
	root := t.TempDir()
	opts := GoombaOptions{PythonVersion: "3.11", Channels: []string{ch.url}, Client: ch.srv.Client()}
	// Warm the package cache, so that the Timeout below is spent in pip.
	if _, err := CreateEnvironmentGoombaWithOptions(context.Background(), "warm", root, opts, nil); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		timeout time.Duration
		cancel  bool
		want    error
	}{
		{"cancelled", 0, true, context.Canceled},
		{"timeout", 6 * time.Second, false, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pip, dir := ctxPip(t, "echo $$ > pip.pid\nsleep 60 &\necho $! > child.pid\necho started\nwait\n")
			orig := pipExecutable
			pipExecutable = func(*PythonEnvironment) string { return pip.PipPath }
			defer func() { pipExecutable = orig }()

			name := "slowpip-" + tc.name
			spec := filepath.Join(t.TempDir(), name+".json")
			writeJSON(t, spec, EnvironmentSpec{Name: name, Channels: []string{ch.url},
				Packages: []PackageSpec{pinned(python), {Name: "requests", Version: "2.31.0", Source: "pip"}}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			start := time.Now()
			_, err := CreateEnvironmentFromJSONFileGoomba(ctx, spec, root, RestoreOptions{},
				GoombaOptions{Client: ch.srv.Client(), Timeout: tc.timeout},
				func(msg string, current, total int64) {
					if tc.cancel && strings.HasPrefix(msg, "Installing pip package requests") {
						cancel()
					}
				})
			elapsed := time.Since(start)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if elapsed > tc.timeout+10*time.Second {
				t.Errorf("returned after %v: the pip step did not stop", elapsed)
			}
			if _, err := os.Lstat(filepath.Join(root, "envs", name)); !os.IsNotExist(err) {
				t.Errorf("the environment was left (%v)", err)
			}
			for _, f := range []string{"pip.pid", "child.pid"} {
				if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
					t.Fatalf("the fake pip never ran (%v)", err)
				}
				if pid := readPID(t, dir, f); !gone(t, pid) {
					t.Errorf("%s (%d) still runs", f, pid)
				}
			}
		})
	}
}
