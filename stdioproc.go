package jumpboot

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// stdioProcess is a RuntimeProcess backed by an OS subprocess that speaks the
// jumpboot queue protocol over its standard input and output. It is used by
// runtimes whose entire program is passed on the command line — Deno and Julia
// — which leaves stdin/stdout free to carry the protocol. The subprocess's
// stderr carries its diagnostics and is forwarded to the host's stderr.
//
// It implements RuntimeProcess and shares the exit-tracking machinery pattern
// with PythonProcess and NodeProcess.
type stdioProcess struct {
	Cmd *exec.Cmd

	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	// OnExit is called once when the process exits.
	OnExit ProcessExitHandler

	exitOnce sync.Once
	exited   bool
	exitErr  error
	exitMu   sync.RWMutex
	exitChan chan struct{}
}

// Compile-time assertion that *stdioProcess satisfies RuntimeProcess.
var _ RuntimeProcess = (*stdioProcess)(nil)

// newStdioProcess wires up stdin/stdout/stderr pipes for cmd, starts it, and
// returns a stdioProcess. The caller has already populated cmd.Path/Args/Env.
func newStdioProcess(cmd *exec.Cmd) (*stdioProcess, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	sp := &stdioProcess{Cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}

	// Terminate the child if the parent receives a termination signal.
	signalChan := make(chan os.Signal, 1)
	setSignalsForChannel(signalChan)
	go func() {
		<-signalChan
		sp.Terminate()
	}()

	return sp, nil
}

// --- RuntimeProcess interface ---

// Transport returns a MessagePack transport over the subprocess's stdio:
// the host reads the child's stdout and writes the child's stdin.
func (sp *stdioProcess) Transport() Transport {
	return NewMsgpackTransport(sp.stdout, sp.stdin)
}

// StdoutReader returns an empty stream: a stdio-protocol runtime's stdout
// carries the queue protocol and must not be forwarded to the host's stdout.
func (sp *stdioProcess) StdoutReader() io.ReadCloser {
	return io.NopCloser(strings.NewReader(""))
}

// StderrReader returns the subprocess's stderr stream for forwarding.
func (sp *stdioProcess) StderrReader() io.ReadCloser { return sp.stderr }

// SetExitHandler registers the callback invoked when the process exits.
func (sp *stdioProcess) SetExitHandler(h ProcessExitHandler) { sp.OnExit = h }

// MonitorProcess starts a background goroutine that waits for the process to
// exit, records its state, and invokes OnExit. Safe to call multiple times.
func (sp *stdioProcess) MonitorProcess() {
	sp.exitOnce.Do(func() {
		sp.exitChan = make(chan struct{})
		go func() {
			err := sp.Cmd.Wait()
			sp.exitMu.Lock()
			sp.exited = true
			sp.exitErr = err
			sp.exitMu.Unlock()
			close(sp.exitChan)

			if sp.OnExit != nil {
				sp.OnExit(err)
			}
		}()
	})
}

// Alive returns true if the subprocess is still running.
func (sp *stdioProcess) Alive() bool {
	sp.exitMu.RLock()
	defer sp.exitMu.RUnlock()
	return !sp.exited
}

// ExitError returns the error from process exit, or nil if it exited cleanly
// or is still running.
func (sp *stdioProcess) ExitError() error {
	sp.exitMu.RLock()
	defer sp.exitMu.RUnlock()
	return sp.exitErr
}

// ExitChan returns a channel closed when the process exits. Returns nil if
// MonitorProcess has not been called.
func (sp *stdioProcess) ExitChan() <-chan struct{} {
	return sp.exitChan
}

// Wait blocks until the subprocess exits and returns its exit error.
func (sp *stdioProcess) Wait() error {
	if sp.exitChan != nil {
		<-sp.exitChan
		return sp.ExitError()
	}
	return sp.Cmd.Wait()
}

// Terminate gracefully stops the subprocess with SIGTERM, escalating to
// SIGKILL after 5 seconds. Returns nil if the process was not running.
func (sp *stdioProcess) Terminate() error {
	if sp.Cmd.Process == nil {
		return nil
	}
	if !sp.Alive() {
		return sp.ExitError()
	}

	if err := sp.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}

	var done <-chan struct{}
	if sp.exitChan != nil {
		done = sp.exitChan
	} else {
		d := make(chan struct{}, 1)
		go func() {
			sp.Cmd.Wait()
			close(d)
		}()
		done = d
	}

	select {
	case <-time.After(5 * time.Second):
		if err := sp.Cmd.Process.Kill(); err != nil {
			return err
		}
		<-done
	case <-done:
	}

	return sp.ExitError()
}
