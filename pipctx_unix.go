//go:build !windows

package jumpboot

import (
	"errors"
	"os/exec"
	"syscall"
)

// setPipProcessGroup starts pip in a process group of its own and makes
// cancellation kill the whole group, so that the build backends pip runs
// die with it.
func setPipProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}
