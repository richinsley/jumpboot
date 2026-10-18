package jumpboot

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// probeOutputLimit bounds the standard output kept from a version probe.
const probeOutputLimit = 64 << 10

// probeExecutable returns the program a version probe runs for path, an
// environment's python or pip; tests replace it.
var probeExecutable = func(path string) string { return path }

// cappedBuffer keeps the first limit bytes written to it and discards the
// rest, so that a process writing without end neither blocks nor fills
// memory.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// runProbeContext runs path with args, as groupCommand runs commands, and
// returns at most probeOutputLimit bytes of its standard output. When ctx
// is done, the probe and the processes it started are killed and the
// error wraps ctx.Err().
func runProbeContext(ctx context.Context, path string, args ...string) (string, error) {
	cmd := groupCommand(ctx, probeExecutable(path), args...)
	out := &cappedBuffer{limit: probeOutputLimit}
	cmd.Stdout = out
	err := cmd.Run()
	if cerr := ctx.Err(); cerr != nil {
		return "", fmt.Errorf("%s stopped: %w", filepath.Base(path), cerr)
	}
	return out.buf.String(), err
}

// checkCondaEnvironmentContext is checkCondaEnvironment for the goomba
// backend: the same checks and messages, with the python and pip version
// probes stopped (with their process groups) when ctx is done.
func checkCondaEnvironmentContext(ctx context.Context, env *PythonEnvironment, requestedVersion Version) error {
	pver, err := runProbeContext(ctx, env.PythonPath, "--version")
	if err != nil {
		return fmt.Errorf("error running python --version: %w", err)
	}
	env.PythonVersion, err = ParsePythonVersion(pver)
	if err != nil {
		return fmt.Errorf("error parsing Python version: %w", err)
	}
	if _, err := os.Stat(env.PythonLibPath); os.IsNotExist(err) {
		env.PythonLibPath = ""
	}
	pipver, err := runProbeContext(ctx, env.PipPath, "--version")
	if err != nil {
		return fmt.Errorf("error running pip --version: %w", err)
	}
	env.PipVersion, err = ParsePipVersion(pipver)
	if err != nil {
		return fmt.Errorf("error parsing pip version: %w", err)
	}
	if env.PythonVersion.Compare(requestedVersion) < 0 {
		return fmt.Errorf("requested python version %s is not available, found %s", requestedVersion.String(), env.PythonVersion.String())
	}
	return nil
}
