//go:build windows
// +build windows

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

func setSignalsForChannel(c chan os.Signal) {
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
}

func waitForExit(cmd *exec.Cmd) error {
	err := cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ProcessState.ExitCode() == -1 {
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
	cmd.SysProcAttr.HideWindow = true
}

func terminateProcess(cmd *exec.Cmd, done <-chan struct{}) error {
	if cmd.Process == nil {
		return nil
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}

	select {
	case <-time.After(5 * time.Second):
		if err := cmd.Process.Kill(); err != nil {
			return err
		}
		<-done
	case <-done:
	}

	return nil
}

// return the file descriptors as numerical strings
func setExtraFiles(cmd *exec.Cmd, extraFiles []*os.File) []string {
	retv := make([]string, len(extraFiles))
	var handles []syscall.Handle
	for i, f := range extraFiles {
		handles = append(handles, syscall.Handle(f.Fd()))
		retv[i] = fmt.Sprintf("%d", f.Fd())
	}

	// Pass the handle to the child process
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.NoInheritHandles = false
	cmd.SysProcAttr.AdditionalInheritedHandles = handles
	return retv
}
