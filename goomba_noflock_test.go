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
