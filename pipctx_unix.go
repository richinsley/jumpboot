//go:build !windows

package jumpboot

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcessGroup starts cmd in a process group of its own and makes
// cancellation kill the whole group, so that the processes cmd starts
// (pip's build backends, say) die with it.
func setProcessGroup(cmd *exec.Cmd) {
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
