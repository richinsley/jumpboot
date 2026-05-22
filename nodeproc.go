package jumpboot

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

//go:embed scripts/bootstrap.js
var primaryNodeBootstrapScript string

//go:embed scripts/secondaryBootstrapScript.js
var secondaryNodeBootstrapScript string

//go:embed packages/jumpboot-js/*
var jumpbootJSPackage embed.FS

// NodeProgram defines a complete Node.js program to be executed: the main
// module, supporting packages and modules, and configuration. It is the
// Node.js counterpart of PythonProgram and is serialized to JSON for the
// secondary bootstrap (scripts/secondaryBootstrapScript.js), which reconstructs
// the modules in memory and runs the main one.
//
// Module and Package are reused from the Python side — base64-encoded source
// is language-neutral.
type NodeProgram struct {
	// Name identifies the program (used for logging and debugging).
	Name string

	// Path is the base path for resolving relative imports.
	Path string

	// Program is the main module to execute.
	Program Module

	// Packages contains JS packages available for require().
	Packages []Package

	// Modules contains standalone JS modules available for require().
	Modules []Module

	// PipeIn is the fd the Node process reads commands from (set automatically).
	PipeIn int

	// PipeOut is the fd the Node process writes responses to (set automatically).
	PipeOut int

	// StatusIn is the fd for status/exception reporting (set automatically).
	StatusIn int

	// DebugPort, if non-zero, is reserved for future Node inspector support.
	DebugPort int

	// BreakOnStart is reserved for future Node inspector support.
	BreakOnStart bool

	// KVPairs contains key-value data reachable in Node as jumpboot.<key>.
	KVPairs map[string]interface{}
}

// NodeProcess represents a running Node.js subprocess with communication pipes.
// It mirrors PythonProcess and implements the RuntimeProcess interface so that
// QueueProcess can drive it without knowing it is Node.js.
type NodeProcess struct {
	// Cmd is the underlying exec.Cmd for the Node process.
	Cmd *exec.Cmd

	// Stdin is the write end of the process's standard input.
	Stdin io.WriteCloser

	// Stdout is the read end of the process's standard output.
	Stdout io.ReadCloser

	// Stderr is the read end of the process's standard error.
	Stderr io.ReadCloser

	// PipeIn is for reading data sent from the Node process.
	PipeIn *os.File

	// PipeOut is for writing data to the Node process.
	PipeOut *os.File

	// StatusIn receives status messages and exceptions from Node.
	StatusIn *os.File

	// ExceptionChan receives Node exceptions reported via the status pipe.
	ExceptionChan chan *NodeException

	// StatusChan receives status messages (e.g., "exit") from Node.
	StatusChan chan map[string]interface{}

	// OnExit is called when the process exits.
	OnExit ProcessExitHandler

	exitOnce sync.Once
	exited   bool
	exitErr  error
	exitMu   sync.RWMutex
	exitChan chan struct{}
}

// Compile-time assertion that *NodeProcess satisfies RuntimeProcess.
var _ RuntimeProcess = (*NodeProcess)(nil)

// NewNodeProcessFromProgram starts a Node.js process running the given program.
// It mirrors PythonEnvironment.NewPythonProcessFromProgram: it prepends the
// embedded jumpboot SDK, creates the communication pipes, launches node with
// the primary bootstrap, and streams the secondary bootstrap and program data.
//
// Returns the NodeProcess, the JSON-encoded program data, and any error.
func (env *NodeEnvironment) NewNodeProcessFromProgram(program *NodeProgram, environment_vars map[string]string, extrafiles []*os.File, args ...string) (*NodeProcess, []byte, error) {
	// Prepend the embedded jumpboot-js SDK package.
	jsPkg, err := newPackageFromFS("jumpboot", "jumpboot", "packages/jumpboot-js", jumpbootJSPackage, ".js")
	if err != nil {
		return nil, nil, err
	}
	program.Packages = append([]Package{*jsPkg}, program.Packages...)

	// Create the bootstrap and program-data pipes (closed after writing).
	reader_bootstrap, writer_bootstrap, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	reader_program, writer_program, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}

	// Create the primary data and status pipes.
	pipein_reader_primary, pipein_writer_primary, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	pipeout_reader_primary, pipeout_writer_primary, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	status_reader_primary, status_writer_primary, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}

	cmd := exec.Command(env.NodePath)

	// Pass the pipes as inherited file descriptors. Order matches the Python
	// side: data-out, data-in, status, bootstrap, program, then user extras.
	extradescriptors := setExtraFiles(cmd, append([]*os.File{
		pipein_writer_primary, pipeout_reader_primary, status_writer_primary,
		reader_bootstrap, reader_program,
	}, extrafiles...))

	program.PipeOut, _ = strconv.Atoi(extradescriptors[0])
	program.PipeIn, _ = strconv.Atoi(extradescriptors[1])
	program.StatusIn, _ = strconv.Atoi(extradescriptors[2])
	extradescriptors = extradescriptors[3:]

	// node -e <primary bootstrap> <extra-fd count> <bootstrap fd> <program fd> ...
	cmd.Args = append(cmd.Args, "-e", primaryNodeBootstrapScript)
	cmd.Args = append(cmd.Args, fmt.Sprintf("%d", len(extradescriptors)))
	cmd.Args = append(cmd.Args, extradescriptors...)
	cmd.Args = append(cmd.Args, args...)

	cmd.Env = os.Environ()
	for key, value := range environment_vars {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}

	programData, err := json.Marshal(program)
	if err != nil {
		return nil, nil, err
	}

	// Status pipe reader: surfaces status messages and exceptions from Node.
	schan := make(chan map[string]interface{}, 1)
	echan := make(chan *NodeException, 1)
	go func() {
		defer status_writer_primary.Close()
		statusScanner := bufio.NewScanner(status_reader_primary)
		for statusScanner.Scan() {
			var status map[string]interface{}
			text := statusScanner.Text()
			if err := json.Unmarshal([]byte(text), &status); err != nil {
				log.Printf("Error decoding Node status JSON: %v, data: %s", err, text)
				break
			}
			if status["type"] == "status" {
				schan <- status
				if status["message"] == "exit" {
					break
				}
			} else if status["type"] == "exception" {
				exception, err := NewNodeExceptionFromJSON(statusScanner.Bytes())
				if err != nil {
					log.Printf("Error decoding Node exception: %v, %s", err, text)
					continue
				}
				echan <- exception
			} else {
				log.Printf("Unknown Node status type: %s", text)
			}
		}
	}()

	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	// Stream the secondary bootstrap and the program data, then close.
	go func() {
		defer writer_bootstrap.Close()
		io.WriteString(writer_bootstrap, secondaryNodeBootstrapScript)
	}()
	go func() {
		defer writer_program.Close()
		writer_program.Write(programData)
	}()

	nodeProcess := &NodeProcess{
		Cmd:           cmd,
		Stdin:         stdinPipe,
		Stdout:        stdoutPipe,
		Stderr:        stderrPipe,
		PipeIn:        pipein_reader_primary,
		PipeOut:       pipeout_writer_primary,
		StatusIn:      status_reader_primary,
		ExceptionChan: echan,
		StatusChan:    schan,
	}

	// Terminate the child if the parent receives a termination signal.
	signalChan := make(chan os.Signal, 1)
	setSignalsForChannel(signalChan)
	go func() {
		<-signalChan
		nodeProcess.Terminate()
	}()

	return nodeProcess, programData, nil
}

// --- RuntimeProcess interface ---

// Transport returns a MessagePack transport over the process's data pipes.
func (np *NodeProcess) Transport() Transport {
	return NewMsgpackTransport(np.PipeIn, np.PipeOut)
}

// StdoutReader returns the process's standard output stream.
func (np *NodeProcess) StdoutReader() io.ReadCloser { return np.Stdout }

// StderrReader returns the process's standard error stream.
func (np *NodeProcess) StderrReader() io.ReadCloser { return np.Stderr }

// SetExitHandler registers the callback invoked when the process exits.
func (np *NodeProcess) SetExitHandler(h ProcessExitHandler) { np.OnExit = h }

// MonitorProcess starts a background goroutine that waits for the process to
// exit, records its state, and invokes OnExit. Safe to call multiple times.
func (np *NodeProcess) MonitorProcess() {
	np.exitOnce.Do(func() {
		np.exitChan = make(chan struct{})
		go func() {
			err := np.Cmd.Wait()
			np.exitMu.Lock()
			np.exited = true
			np.exitErr = err
			np.exitMu.Unlock()
			close(np.exitChan)

			if np.OnExit != nil {
				np.OnExit(err)
			}
		}()
	})
}

// Alive returns true if the Node process is still running.
func (np *NodeProcess) Alive() bool {
	np.exitMu.RLock()
	defer np.exitMu.RUnlock()
	return !np.exited
}

// ExitError returns the error from process exit, or nil if it exited cleanly
// or is still running.
func (np *NodeProcess) ExitError() error {
	np.exitMu.RLock()
	defer np.exitMu.RUnlock()
	return np.exitErr
}

// ExitChan returns a channel closed when the process exits. Returns nil if
// MonitorProcess has not been called.
func (np *NodeProcess) ExitChan() <-chan struct{} {
	return np.exitChan
}

// Wait blocks until the Node process exits and returns its exit error.
func (np *NodeProcess) Wait() error {
	if np.exitChan != nil {
		<-np.exitChan
		return np.ExitError()
	}
	return np.Cmd.Wait()
}

// Terminate gracefully stops the Node process with SIGTERM, escalating to
// SIGKILL after 5 seconds. Returns nil if the process was not running.
func (np *NodeProcess) Terminate() error {
	if np.Cmd.Process == nil {
		return nil
	}
	if !np.Alive() {
		return np.ExitError()
	}

	if err := np.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}

	var done <-chan struct{}
	if np.exitChan != nil {
		done = np.exitChan
	} else {
		d := make(chan struct{}, 1)
		go func() {
			np.Cmd.Wait()
			close(d)
		}()
		done = d
	}

	select {
	case <-time.After(5 * time.Second):
		if err := np.Cmd.Process.Kill(); err != nil {
			return err
		}
		<-done
	case <-done:
	}

	return np.ExitError()
}
