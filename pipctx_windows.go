//go:build windows

package jumpboot

import "os/exec"

// setProcessGroup keeps exec.CommandContext's default cancellation, which
// kills the command only: the goomba backend, the only user, refuses
// Windows.
func setProcessGroup(cmd *exec.Cmd) {}
