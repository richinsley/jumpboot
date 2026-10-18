package jumpboot

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// pipWaitDelay bounds how long pipInstallContext waits, once ctx is done
// and pip is killed, for pip's output to close and for Wait to return.
const pipWaitDelay = 5 * time.Second

// pipInstallContext installs packages with env's pip as PipInstallPackages
// does (same arguments, one progress call per line of pip's standard
// output, pip's standard error in the error), but stops when ctx is done:
// pip runs in a process group of its own (on Unix), and the whole group is
// killed, so that the build backends pip starts die with it; the error
// then wraps ctx.Err(). pip is always waited for and reaped, also when
// progressCallback panics (which kills pip first; the panic then
// continues).
func pipInstallContext(ctx context.Context, env *PythonEnvironment, packages []string, indexURL, extraIndexURL string, noCache bool, progressCallback ProgressCallback) (err error) {
	args := []string{"install", "--no-warn-script-location"}
	if noCache {
		args = append(args, "--no-cache-dir")
	}
	args = append(args, packages...)
	if indexURL != "" {
		args = append(args, "--index-url", indexURL)
	}
	if extraIndexURL != "" {
		args = append(args, "--extra-index-url", extraIndexURL)
	}

	// cancel also stops pip when the callback panics.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, env.PipPath, args...)
	setPipProcessGroup(cmd)
	// Once ctx is done and pip is killed, os/exec closes pip's output pipes
	// after WaitDelay at the latest, even if a process outside pip's group
	// still holds them, so neither the reading below nor Wait can hang.
	cmd.WaitDelay = pipWaitDelay
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("error creating pip stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("error starting pip install: %w", err)
	}

	waited := false
	defer func() {
		if !waited { // the callback panicked: stop pip, then reap it
			cancel()
			_, _ = io.Copy(io.Discard, stdout)
			_ = cmd.Wait()
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 32*1024), 1024*1024)
	lineCount := int64(0)
	desc := "Installing pip packages..."
	if len(packages) == 1 {
		desc = fmt.Sprintf("Installing pip package %s...", packages[0])
	}
	for scanner.Scan() {
		lineCount++
		if progressCallback != nil {
			progressCallback(desc, lineCount, -1)
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		// Keep draining so pip cannot block on a full pipe before Wait.
		_, _ = io.Copy(io.Discard, stdout)
	}
	waited = true
	waitErr := cmd.Wait()
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("pip install stopped: %w", cerr)
	}
	if waitErr != nil {
		return fmt.Errorf("error installing package: %v, stderr: %s", waitErr, stderr.String())
	}
	if scanErr != nil {
		return fmt.Errorf("error reading pip install output: %w", scanErr)
	}
	if progressCallback != nil {
		progressCallback("Pip packages installed successfully", 100, 100)
	}
	return nil
}
