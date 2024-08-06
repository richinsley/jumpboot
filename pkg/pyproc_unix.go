//go:build !windows
// +build !windows

package pkg

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// The file descriptor is passed as an extra file, so it will be after stderr
//
//go:embed scripts/bootstrap_unix.py
var primaryBootstrapScriptTemplate string

//go:embed scripts/secondaryBootstrapScript_unix.py
var secondaryBootstrapScriptTemplate string

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
