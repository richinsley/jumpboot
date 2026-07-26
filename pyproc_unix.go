//go:build !windows
// +build !windows

package jumpboot

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// setSignalsForChannel configures the channel to receive SIGINT and SIGTERM.
func setSignalsForChannel(c chan os.Signal) {
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
}

// waitForExit waits for a command to exit and returns an appropriate error.
func waitForExit(cmd *exec.Cmd) error {
	err := cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == -1 {
				// The child process was killed
				return errors.New("child process was killed")
			}
		}
		return err
	}
	return nil
}

func prepareProcessCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func terminateProcess(cmd *exec.Cmd, done <-chan struct{}) error {
	if cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	target := -pid
	if pgid, err := syscall.Getpgid(pid); err == nil {
		target = -pgid
	}

	if err := syscall.Kill(target, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}

	select {
	case <-time.After(5 * time.Second):
		if err := syscall.Kill(target, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		<-done
	case <-done:
	}

	return nil
}

// setExtraFiles attaches extra files to the command and returns their FD numbers.
// On Unix, extra files start at FD 3 (after stdin=0, stdout=1, stderr=2).
func setExtraFiles(cmd *exec.Cmd, extraFiles []*os.File) []string {
	cmd.ExtraFiles = extraFiles
	retv := make([]string, len(extraFiles))

	// stdio file descriptors are 0, 1, 2
	// extra file descriptors are 3, 4, 5, ...
	for i, _ := range extraFiles {
		retv[i] = fmt.Sprintf("%d", i+3)
	}
	return retv
}
