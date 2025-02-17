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
)

func setSignalsForChannel(c chan os.Signal) {
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
}

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

// return the file descriptors as numerical strings
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
