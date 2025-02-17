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

// return the file descriptors as numerical strings
func setExtraFiles(cmd *exec.Cmd, extraFiles []*os.File) []string {
	retv := make([]string, len(extraFiles))
	var handles []syscall.Handle
	for i, f := range extraFiles {
		handles = append(handles, syscall.Handle(f.Fd()))
		retv[i] = fmt.Sprintf("%d", f.Fd())
	}

	// Pass the handle to the child process
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Hide the console window
		HideWindow: true,
		// Inherit handles
		NoInheritHandles: false,
		// Pass the handle to the child process
		AdditionalInheritedHandles: handles,
	}
	return retv
}
