package jumpboot

import (
	"bufio"
	_ "embed"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed scripts/repl.js
var nodeReplScript string

// NodeREPLProcess provides interactive JavaScript execution where state
// persists between calls. It is the Node.js counterpart of REPLPythonProcess
// and uses the same delimiter-based protocol (see DELIMITER), so its Execute
// contract matches the Python REPL's.
//
// State persists across Execute calls via a long-lived vm context:
//
//	repl, _ := env.NewREPLProcess(nil, nil, nil, nil)
//	repl.Execute("var x = 42", true)
//	result, _ := repl.Execute("x * 2", true)  // returns "84"
//
// Note: `var`, function/class declarations and bare assignments persist;
// top-level `let`/`const` are scoped to a single Execute call (a JavaScript
// limitation — use `var` or assignment for persistent state).
//
// NodeREPLProcess is safe for concurrent use; Execute calls are serialized via
// an internal mutex.
type NodeREPLProcess struct {
	*NodeProcess

	// m protects concurrent access to the REPL.
	m sync.Mutex

	// closed indicates the REPL has been terminated.
	closed bool

	// combinedOutput tracks whether stdout/stderr are currently combined.
	combinedOutput bool
}

// NewREPLProcess creates a new interactive Node.js REPL process.
//
// Parameters:
//   - kvpairs: key-value data reachable in Node as jumpboot.<key>; may be nil
//   - environment_vars: additional environment variables; may be nil
//   - modules: additional JS modules available for require(); may be nil
//   - packages: additional JS packages available for require(); may be nil
//
// The REPL starts in combined-output mode (console.log and console.error
// merged). Pass combinedOutput=false to Execute to capture only console.log.
func (env *NodeEnvironment) NewREPLProcess(kvpairs map[string]interface{}, environment_vars map[string]string, modules []Module, packages []Package) (*NodeREPLProcess, error) {
	if modules == nil {
		modules = []Module{}
	}
	if packages == nil {
		packages = []Package{}
	}
	program := &NodeProgram{
		Name:     "JumpBootNodeREPL",
		Path:     ".",
		Program:  *NewModuleFromString("__main__", "repl.js", nodeReplScript),
		Modules:  modules,
		Packages: packages,
		KVPairs:  kvpairs,
	}

	process, _, err := env.NewNodeProcessFromProgram(program, environment_vars, nil)
	if err != nil {
		return nil, err
	}
	process.MonitorProcess()

	// The REPL captures console output itself and exchanges it over the data
	// pipes; the process's real stdout/stderr should still be drained so a
	// stray write cannot fill an undrained pipe and stall the process.
	go io.Copy(os.Stdout, process.Stdout)
	go io.Copy(os.Stderr, process.Stderr)

	return &NodeREPLProcess{
		NodeProcess:    process,
		combinedOutput: true,
	}, nil
}

// Execute runs JavaScript code in the REPL and returns the captured output.
//
// Parameters:
//   - code: JavaScript source to execute (may be multi-line)
//   - combinedOutput: if true, console.error/warn are captured alongside
//     console.log; if false, only console.log is captured
//
// Execute blocks until the code completes and all output is received. The REPL
// maintains state between calls. The value of a trailing expression is appended
// to the output, the way an interactive REPL echoes results.
//
// Returns an error if the REPL is closed, on a communication error, or if the
// JavaScript threw (the returned output still contains the stack trace).
func (rp *NodeREPLProcess) Execute(code string, combinedOutput bool) (string, error) {
	rp.m.Lock()
	defer rp.m.Unlock()

	if rp.closed {
		return "", fmt.Errorf("REPL process has been closed")
	}

	if rp.combinedOutput != combinedOutput {
		cc := "__CAPTURE_COMBINED__ ="
		if combinedOutput {
			cc += " True" + DELIMITER
		} else {
			cc += " False" + DELIMITER
		}
		if _, err := rp.NodeProcess.PipeOut.WriteString(cc); err != nil {
			return "", err
		}
		rp.combinedOutput = combinedOutput
	}

	code = strings.ReplaceAll(code, "\r\n", "\n")
	code = strings.TrimRight(code, " \t\n\r")
	code += DELIMITER

	if _, err := rp.NodeProcess.PipeOut.WriteString(code); err != nil {
		return "", err
	}

	// A status (or exception) arrives once the block has run.
	var exerr error
	select {
	case <-rp.StatusChan:
	case e := <-rp.ExceptionChan:
		exerr = e // *NodeException implements the error interface
	}

	// Read the captured output up to the delimiter.
	reader := bufio.NewReader(rp.NodeProcess.PipeIn)
	var result strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		result.WriteString(line)

		if strings.HasSuffix(result.String(), DELIMITER) {
			output := strings.TrimSuffix(result.String(), DELIMITER)
			output = strings.TrimRight(output, "\n\r")
			return output, exerr
		}
		if err == io.EOF {
			return "", fmt.Errorf("unexpected EOF")
		}
	}
}

// ExecuteWithTimeout runs JavaScript code with a maximum execution time. If the
// timeout is exceeded the Node process is terminated and the REPL is marked
// closed; create a new REPL to continue.
func (rp *NodeREPLProcess) ExecuteWithTimeout(code string, combinedOutput bool, timeout time.Duration) (string, error) {
	rp.m.Lock()
	defer rp.m.Unlock()

	if rp.closed {
		return "", fmt.Errorf("REPL process has been closed")
	}

	if rp.combinedOutput != combinedOutput {
		cc := "__CAPTURE_COMBINED__ ="
		if combinedOutput {
			cc += " True" + DELIMITER
		} else {
			cc += " False" + DELIMITER
		}
		if _, err := rp.NodeProcess.PipeOut.WriteString(cc); err != nil {
			return "", err
		}
		rp.combinedOutput = combinedOutput
	}

	code = strings.ReplaceAll(code, "\r\n", "\n")
	code = strings.TrimRight(code, " \t\n\r")
	code += DELIMITER

	if _, err := rp.NodeProcess.PipeOut.WriteString(code); err != nil {
		return "", err
	}

	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		select {
		case <-rp.StatusChan:
		case <-rp.ExceptionChan:
		}
		reader := bufio.NewReader(rp.NodeProcess.PipeIn)
		var result strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil && err != io.EOF {
				errCh <- err
				return
			}
			result.WriteString(line)
			if strings.HasSuffix(result.String(), DELIMITER) {
				output := strings.TrimSuffix(result.String(), DELIMITER)
				resultCh <- strings.TrimRight(output, "\n\r")
				return
			}
			if err == io.EOF {
				errCh <- fmt.Errorf("unexpected EOF")
				return
			}
		}
	}()

	select {
	case output := <-resultCh:
		return output, nil
	case err := <-errCh:
		return "", err
	case <-time.After(timeout):
		rp.NodeProcess.Terminate()
		rp.closed = true
		return "", fmt.Errorf("execution timed out - Node process terminated")
	}
}

// Close terminates the Node REPL process and releases resources. After Close
// the REPL cannot be reused.
//
// It closes the pipe the REPL reads from, which gives the REPL an EOF so it
// can exit cleanly; if that does not complete promptly it force-terminates
// the process as a backstop.
func (rp *NodeREPLProcess) Close() error {
	rp.m.Lock()
	defer rp.m.Unlock()

	if rp.closed {
		return fmt.Errorf("REPL process has been closed")
	}
	rp.closed = true

	// EOF on the REPL's input pipe lets it return from its read loop and exit.
	rp.NodeProcess.PipeOut.Close()
	if rp.NodeProcess.exitChan != nil {
		select {
		case <-rp.NodeProcess.exitChan:
			return nil
		case <-time.After(2 * time.Second):
		}
	}
	return rp.NodeProcess.Terminate()
}
