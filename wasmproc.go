package jumpboot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// WasmProcess runs a compiled jumpboot plugin in-process with the wazero
// WebAssembly runtime. Unlike PythonProcess and NodeProcess there is no OS
// subprocess: the plugin is a wasip1/wasm module executing inside the host,
// sandboxed by wazero's capability model.
//
// It implements the RuntimeProcess interface, so QueueProcess drives it
// exactly as it drives the subprocess runtimes. The queue protocol travels
// over the module's WASI stdin/stdout, wired here to in-memory pipes — the
// "virtual pipes" the RuntimeProcess seam was designed to accommodate.
type WasmProcess struct {
	runtime   wazero.Runtime
	compiled  wazero.CompiledModule
	modConfig wazero.ModuleConfig
	ctx       context.Context

	// stdin: host writes stdinW, the guest reads stdinR (its WASI stdin).
	stdinR *io.PipeReader
	stdinW *io.PipeWriter
	// stdout: the guest writes stdoutW (its WASI stdout), host reads stdoutR.
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter

	// OnExit is called once when the module exits.
	OnExit ProcessExitHandler

	exitOnce sync.Once
	exited   bool
	exitErr  error
	exitMu   sync.RWMutex
	exitChan chan struct{}
}

// Compile-time assertion that *WasmProcess satisfies RuntimeProcess.
var _ RuntimeProcess = (*WasmProcess)(nil)

// NewWasmProcessFromProgram compiles the plugin to wasip1/wasm and prepares a
// WasmProcess. The module does not start running until MonitorProcess is
// called (which QueueProcess does during construction).
func (env *WasmEnvironment) NewWasmProcessFromProgram(program *WasmProgram) (*WasmProcess, error) {
	wasmBytes, err := env.compilePlugin(program)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	compiled, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("compiling wasm module: %w", err)
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	name := program.Name
	if name == "" {
		name = "jumpboot-wasm-plugin"
	}
	modConfig := wazero.NewModuleConfig().
		WithName(name).
		WithStdin(stdinR).
		WithStdout(stdoutW).
		WithStderr(os.Stderr)

	return &WasmProcess{
		runtime:   rt,
		compiled:  compiled,
		modConfig: modConfig,
		ctx:       ctx,
		stdinR:    stdinR,
		stdinW:    stdinW,
		stdoutR:   stdoutR,
		stdoutW:   stdoutW,
	}, nil
}

// --- RuntimeProcess interface ---

// Transport returns a MessagePack transport over the module's WASI stdio:
// the host reads the guest's stdout and writes the guest's stdin.
func (wp *WasmProcess) Transport() Transport {
	return NewMsgpackTransport(wp.stdoutR, wp.stdinW)
}

// StdoutReader returns an empty stream: a WASM plugin's stdout carries the
// queue protocol and must not be forwarded to the host's stdout.
func (wp *WasmProcess) StdoutReader() io.ReadCloser {
	return io.NopCloser(strings.NewReader(""))
}

// StderrReader returns an empty stream: the guest's stderr is wired straight
// to the host's stderr by the module config, so there is nothing to forward.
func (wp *WasmProcess) StderrReader() io.ReadCloser {
	return io.NopCloser(strings.NewReader(""))
}

// SetExitHandler registers the callback invoked when the module exits.
func (wp *WasmProcess) SetExitHandler(h ProcessExitHandler) { wp.OnExit = h }

// MonitorProcess starts the wasm module on a background goroutine and records
// its exit. InstantiateModule runs the module's _start and blocks for the
// lifetime of the plugin, so it must run on its own goroutine. Idempotent.
func (wp *WasmProcess) MonitorProcess() {
	wp.exitOnce.Do(func() {
		wp.exitChan = make(chan struct{})
		go func() {
			_, err := wp.runtime.InstantiateModule(wp.ctx, wp.compiled, wp.modConfig)

			wp.exitMu.Lock()
			wp.exited = true
			wp.exitErr = normalizeWasmExit(err)
			wp.exitMu.Unlock()

			// Closing the guest's stdout end unblocks the host transport
			// reader; closing the guest's stdin end unblocks any pending
			// host write.
			wp.stdoutW.Close()
			wp.stdinR.Close()
			close(wp.exitChan)

			if wp.OnExit != nil {
				wp.OnExit(wp.exitErr)
			}
		}()
	})
}

// Alive reports whether the wasm module is still running.
func (wp *WasmProcess) Alive() bool {
	wp.exitMu.RLock()
	defer wp.exitMu.RUnlock()
	return !wp.exited
}

// ExitError returns the error from module exit, or nil if it exited cleanly
// or is still running.
func (wp *WasmProcess) ExitError() error {
	wp.exitMu.RLock()
	defer wp.exitMu.RUnlock()
	return wp.exitErr
}

// ExitChan returns a channel closed when the module exits. Returns nil if
// MonitorProcess has not been called.
func (wp *WasmProcess) ExitChan() <-chan struct{} {
	return wp.exitChan
}

// Wait blocks until the wasm module exits and returns its exit error.
func (wp *WasmProcess) Wait() error {
	if wp.exitChan != nil {
		<-wp.exitChan
		return wp.ExitError()
	}
	return nil
}

// Terminate stops the wasm module. It closes the guest's stdin so a module
// blocked reading the next request unwinds cleanly, then closes the wazero
// runtime as a hard backstop, and waits briefly for the exit to register.
func (wp *WasmProcess) Terminate() error {
	if !wp.Alive() {
		return wp.ExitError()
	}

	// Closing stdin gives a guest parked in a read an EOF so it can exit.
	wp.stdinW.Close()
	// Closing the runtime forcibly unwinds the module if it is still busy.
	wp.runtime.Close(wp.ctx)

	if wp.exitChan != nil {
		select {
		case <-wp.exitChan:
		case <-time.After(5 * time.Second):
		}
	}
	return wp.ExitError()
}

// normalizeWasmExit maps a wazero InstantiateModule result to an exit error.
// A WASI proc_exit with code 0 — the path taken when the guest's main returns
// or it handles the "exit" command — is a clean exit (nil).
func normalizeWasmExit(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *sys.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 0 {
			return nil
		}
		return fmt.Errorf("wasm module exited with code %d", exitErr.ExitCode())
	}
	return err
}
