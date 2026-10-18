//go:build windows

package jumpboot

import "os/exec"

// setPipProcessGroup keeps exec.CommandContext's default cancellation,
// which kills pip only: the goomba backend, the only user, refuses Windows.
func setPipProcessGroup(cmd *exec.Cmd) {}
