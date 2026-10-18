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

// childWaitDelay bounds how long a command from groupCommand is waited
// for, once ctx is done and its process group is killed, for its output to
// close and for Wait to return.
const childWaitDelay = 5 * time.Second

// groupCommand returns a command for name that runs in a process group of
// its own (on Unix) and is killed with its whole group when ctx is done, so
// that the processes it starts die with it. Once ctx is done, os/exec
// closes the command's output pipes after childWaitDelay at the latest,
// even if a process outside the group still holds them, so neither reading
// its output nor Wait can hang.
func groupCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	setProcessGroup(cmd)
	cmd.WaitDelay = childWaitDelay
	return cmd
}

// pipExecutable returns the pip program pipInstallContext runs for env;
// tests replace it.
var pipExecutable = func(env *PythonEnvironment) string { return env.PipPath }

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
	cmd := groupCommand(ctx, pipExecutable(env), args...)
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
