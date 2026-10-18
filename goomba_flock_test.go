//go:build linux || darwin

package jumpboot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/richinsley/goomba"
)

// testPrefixLocked restores into an empty environment directory that
// another installation holds locked, as goomba locks a prefix (an
// exclusive flock on the directory): goomba.ErrPrefixLocked must reach the
// caller, and the directory must be left as it was.
func testPrefixLocked(t *testing.T, ctx context.Context, root string, ch *refChannel, python map[string]any) {
	t.Helper()
	prefix := filepath.Join(root, "envs", "locked")
	if err := os.MkdirAll(prefix, 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := os.Open(prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := syscall.Flock(int(d.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(d.Fd()), syscall.LOCK_UN)
	p := filepath.Join(t.TempDir(), "locked.json")
	writeJSON(t, p, EnvironmentSpec{Name: "locked", PythonVersion: "3.11", Channels: []string{ch.url}, Packages: []PackageSpec{pinned(python)}})
	_, err = CreateEnvironmentFromJSONFileGoomba(ctx, p, root, RestoreOptions{}, GoombaOptions{Client: ch.srv.Client()}, nil)
	if !errors.Is(err, goomba.ErrPrefixLocked) {
		t.Fatalf("locked: err = %v, want goomba.ErrPrefixLocked", err)
	}
	if names, err := d.Readdirnames(-1); err != nil || len(names) != 0 {
		t.Errorf("the locked directory holds %q (%v)", names, err)
	}
}

// lockPrefix takes an exclusive flock on the directory p through its own
// open file description, as a goomba installation or removal in another
// process holds it, until the test ends.
func lockPrefix(t *testing.T, p string) {
	t.Helper()
	d, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(d.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		d.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		syscall.Flock(int(d.Fd()), syscall.LOCK_UN)
		d.Close()
	})
}

// TestGoombaBusyEnvironment checks that an environment another process
// holds locked, finished or not, is reported as goomba.ErrPrefixLocked by
// every goomba function, and left alone.
func TestGoombaBusyEnvironment(t *testing.T) {
	root := t.TempDir()
	fakeCondaEnv(t, root, "done", "3.11.9", "24.0")
	interruptedEnv(t, root, "half")
	for _, name := range []string{"done", "half"} {
		env := filepath.Join(root, "envs", name)
		lockPrefix(t, env)
		_, err := CreateEnvironmentGoomba(name, root, "3.11", "", nil)
		if !errors.Is(err, goomba.ErrPrefixLocked) || errors.Is(err, ErrIncompleteEnvironment) || !strings.Contains(err.Error(), "another process is installing or removing it") {
			t.Errorf("%s: create: err = %v, want goomba.ErrPrefixLocked", name, err)
		}
		spec := filepath.Join(t.TempDir(), "env.json")
		writeJSON(t, spec, EnvironmentSpec{Name: name, PythonVersion: "3.11"})
		if _, err := CreateEnvironmentFromJSONFileGoomba(context.Background(), spec, root, RestoreOptions{}, GoombaOptions{}, nil); !errors.Is(err, goomba.ErrPrefixLocked) {
			t.Errorf("%s: restore: err = %v, want goomba.ErrPrefixLocked", name, err)
		}
		g := &PythonEnvironment{BaseEnvironment: BaseEnvironment{EnvironmentName: name, RootDir: root, EnvPath: env, Backend: BackendGoomba}}
		if err := g.FreezeToFile(filepath.Join(t.TempDir(), "out.json")); !errors.Is(err, goomba.ErrPrefixLocked) {
			t.Errorf("%s: freeze: err = %v, want goomba.ErrPrefixLocked", name, err)
		}
		if err := RemoveIncompleteEnvironment(name, root); !errors.Is(err, goomba.ErrPrefixLocked) {
			t.Errorf("%s: remove: err = %v, want goomba.ErrPrefixLocked", name, err)
		}
		if _, err := os.Stat(filepath.Join(env, "conda-meta")); err != nil {
			t.Errorf("%s: the environment was touched: %v", name, err)
		}
	}
}

// testRestoreLock checks that a restore holds goomba's lock on the new
// environment from the end of the installation through the pip step, so
// that others see it as busy, and that a restore whose lock somebody else
// took first goes on without it.
func testRestoreLock(t *testing.T, ctx context.Context, root string, ch *refChannel, opts GoombaOptions, python map[string]any) {
	t.Helper()
	restore := func(name string, cb ProgressCallback) (*PythonEnvironment, error) {
		p := filepath.Join(t.TempDir(), name+".json")
		writeJSON(t, p, EnvironmentSpec{Name: name, Channels: []string{ch.url}, Packages: []PackageSpec{pinned(python)}})
		return CreateEnvironmentFromJSONFileGoomba(ctx, p, root, RestoreOptions{}, GoombaOptions{Client: ch.srv.Client()}, cb)
	}

	prefix := filepath.Join(root, "envs", "pipbusy")
	var during []error
	_, err := restore("pipbusy", func(msg string, current, total int64) {
		if msg != "Python environment created successfully" || during != nil {
			return
		}
		// Another caller, now: the environment is installed but its pip
		// step has not run.
		_, createErr := CreateEnvironmentGoombaWithOptions(ctx, "pipbusy", root, opts, nil)
		during = []error{goomba.CheckComplete(prefix), createErr, RemoveIncompleteEnvironment("pipbusy", root)}
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(during) != 3 {
		t.Fatal("the restore never reported the environment created")
	}
	for i, what := range []string{"CheckComplete", "create", "RemoveIncompleteEnvironment"} {
		if !errors.Is(during[i], goomba.ErrPrefixLocked) {
			t.Errorf("%s during the pip step: err = %v, want goomba.ErrPrefixLocked", what, during[i])
		}
	}
	if err := goomba.CheckComplete(prefix); err != nil {
		t.Errorf("after the restore: %v, want a finished, unlocked environment", err)
	}

	// Somebody takes the lock between goomba's installation and the
	// restore's own locking: the restore goes on without it.
	var holder *os.File
	restoreAfterInstallHook = func(p string) {
		d, err := os.Open(p)
		if err != nil {
			t.Error(err)
			return
		}
		if err := syscall.Flock(int(d.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			d.Close()
			t.Error(err)
			return
		}
		holder = d
	}
	defer func() { restoreAfterInstallHook = nil }()
	env, err := restore("nolock", nil)
	if holder == nil {
		t.Fatal("the hook did not take the lock")
	}
	syscall.Flock(int(holder.Fd()), syscall.LOCK_UN)
	holder.Close()
	if err != nil || !env.IsNew || env.PipVersion.Major <= 0 {
		t.Errorf("restore without the lock: env %+v, err %v", env, err)
	}
}
