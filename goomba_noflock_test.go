//go:build !(linux || darwin)

package jumpboot

import (
	"context"
	"testing"
)

// testPrefixLocked needs flock; see goomba_flock_test.go.
func testPrefixLocked(t *testing.T, ctx context.Context, root string, ch *refChannel, python map[string]any) {
	t.Log("prefix lock test skipped: no flock on this platform")
}

// testRestoreLock needs flock; see goomba_flock_test.go.
func testRestoreLock(t *testing.T, ctx context.Context, root string, ch *refChannel, opts GoombaOptions, python map[string]any) {
	t.Log("restore lock test skipped: no flock on this platform")
}
