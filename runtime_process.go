package jumpboot

import "io"

// RuntimeProcess is the language-neutral contract that QueueProcess depends on.
//
// It abstracts a running language runtime (a Python subprocess today, a Node.js
// subprocess next, and eventually an in-process WASM module) so that the RPC
// machinery in QueueProcess works against any runtime without knowing which one
// it is. The MessagePack queue wire protocol (see docs/PROTOCOL.md) is the
// portable contract; RuntimeProcess is the lifecycle and transport contract.
//
// Method names are deliberately chosen to avoid collisions with the exported
// fields of PythonProcess (Stdout, Stderr, OnExit, PipeIn, PipeOut): Go forbids
// a method and a field of the same name on one type, and those fields are part
// of the existing public API. Hence StdoutReader/StderrReader/SetExitHandler/
// Transport rather than the bare field names.
//
// *PythonProcess implements RuntimeProcess (see the accessor methods in
// pyproc.go). Future runtimes (NodeProcess, WasmProcess) implement it likewise.
type RuntimeProcess interface {
	// Transport returns the framed message transport for the RPC queue.
	// The process owns transport construction so QueueProcess never touches
	// raw pipes directly — this is what an in-process WASM target (with
	// virtual rather than OS pipes) needs.
	Transport() Transport

	// StdoutReader returns the runtime's standard output stream, for forwarding.
	StdoutReader() io.ReadCloser

	// StderrReader returns the runtime's standard error stream, for forwarding.
	StderrReader() io.ReadCloser

	// SetExitHandler registers a callback invoked once when the process exits.
	// It must be called before MonitorProcess to take effect.
	SetExitHandler(ProcessExitHandler)

	// MonitorProcess starts background monitoring of process exit. Idempotent.
	MonitorProcess()

	// Alive reports whether the process is still running (non-blocking).
	Alive() bool

	// ExitError returns the error from process exit, or nil if the process
	// exited cleanly or is still running.
	ExitError() error

	// ExitChan returns a channel closed when the process exits. May be nil
	// if MonitorProcess has not been called.
	ExitChan() <-chan struct{}

	// Terminate stops the process, escalating to a forceful kill if needed.
	Terminate() error

	// Wait blocks until the process exits and returns its exit error.
	Wait() error
}

// Compile-time assertion that *PythonProcess satisfies RuntimeProcess.
var _ RuntimeProcess = (*PythonProcess)(nil)
