//go:build linux || darwin

package jumpboot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
