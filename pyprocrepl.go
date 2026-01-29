package jumpboot

import (
	"bufio"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed scripts/repl.py
var replScript string

// REPLPythonProcess provides interactive Python code execution where state persists
// between calls. It wraps a PythonProcess with a custom REPL implementation that uses
// delimiter-based communication for reliable output capture.
//
// The REPL maintains Python interpreter state, so variables defined in one Execute
// call remain available in subsequent calls:
//
//	repl, _ := env.NewREPLPythonProcess(nil, nil, nil, nil)
//	repl.Execute("x = 42", true)
//	result, _ := repl.Execute("print(x * 2)", true)  // returns "84"
//
// REPLPythonProcess is safe for concurrent use by multiple goroutines. Execute calls
// are serialized via an internal mutex to prevent interleaving. Note that while Go-side
// access is thread-safe, the underlying Python interpreter is single-threaded, so
// concurrent Execute calls will be serialized on the Python side as well.
type REPLPythonProcess struct {
	*PythonProcess

	// m protects concurrent access to the REPL
	m sync.Mutex

	// closed indicates the REPL has been terminated
	closed bool

	// combinedOutput controls whether stdout/stderr are combined in output
	combinedOutput bool
}

// NewREPLPythonProcess creates a new interactive Python REPL process.
//
// Parameters:
//   - kvpairs: Key-value data accessible in Python as jumpboot.<key>; may be nil
//   - environment_vars: Additional environment variables; may be nil
//   - modules: Additional Python modules available for import; may be nil
//   - packages: Additional Python packages available for import; may be nil
//
// The REPL process starts with combined output mode (stdout and stderr merged).
// Use Execute with combinedOutput=false to capture them separately.
func (env *PythonEnvironment) NewREPLPythonProcess(kvpairs map[string]interface{}, environment_vars map[string]string, modules []Module, packages []Package) (*REPLPythonProcess, error) {
	cwd, _ := os.Getwd()
	if modules == nil {
		modules = []Module{}
	}
	if packages == nil {
		packages = []Package{}
	}
	program := &PythonProgram{
		Name: "JumpBootREPL",
		Path: cwd,
		Program: Module{
			Name:   "__main__",
			Path:   path.Join(cwd, "modules", "repl.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(replScript)),
		},
		Modules:  modules,
		Packages: packages,
		KVPairs:  kvpairs,
		// KVPairs:  map[string]interface{}{"SHARED_MEMORY_NAME": name, "SHARED_MEMORY_SIZE": size, "SEMAPHORE_NAME": semaphore_name},
	}

	process, _, err := env.NewPythonProcessFromProgram(program, environment_vars, nil, false)
	if err != nil {
		return nil, err
	}

	return &REPLPythonProcess{
		PythonProcess:  process,
		closed:         false,
		combinedOutput: true, // the default is to combine stdout and stderr
	}, nil
}

// DELIMITER marks the end of REPL output using non-printable ASCII characters.
// This allows reliable detection of output boundaries without conflicting with user code.
const DELIMITER = "\x01\x02\x03\n"

// WINDELIMITER is the Windows variant with CRLF line endings.
// Windows Python outputs CRLF, but the write delimiter uses LF consistently.
const WINDELIMITER = "\x01\x02\x03\r\n"

// Execute runs Python code in the REPL and returns the captured output.
//
// Parameters:
//   - code: Python source code to execute (may be multi-line)
//   - combinedOutput: If true, stdout and stderr are merged; if false, only stdout
//
// Execute blocks until the code completes and all output is received. The REPL
// maintains state between calls, so variables and imports persist.
//
// Returns an error if the REPL is closed, if there's a communication error, or
// if the Python code raised an exception (the error contains the traceback).
//
// Empty lines in the code are normalized and trailing whitespace is trimmed.
func (rpp *REPLPythonProcess) Execute(code string, combinedOutput bool) (string, error) {
	iswin := runtime.GOOS == "windows"

	// we need to lock the mutex to prevent multiple goroutines from writing to the Python process at the same time
	rpp.m.Lock()
	defer rpp.m.Unlock()

	// check if the Python process has been closed
	if rpp.closed {
		return "", fmt.Errorf("REPL process has been closed")
	}

	// if we are changing the combined output setting, update the Python process
	if rpp.combinedOutput != combinedOutput {
		cc := "__CAPTURE_COMBINED__ ="
		if combinedOutput {
			cc += " True" + DELIMITER
		} else {
			cc += " False" + DELIMITER
		}
		_, err := rpp.PythonProcess.PipeOut.WriteString(cc)
		if err != nil {
			return "", err
		}
		rpp.combinedOutput = combinedOutput
	}

	// remove empty lines from the code - account for \r\n line endings on Windows
	code = strings.ReplaceAll(code, "\r\n", "\n")
	code = strings.ReplaceAll(code, "\n\n", "\n")

	// trim whitespace from the end of the code
	code = strings.TrimRight(code, " \t\n\r")

	// append the DELIMITER to the end of the code
	code += DELIMITER

	// write the code to the Python process as a single string
	_, err := rpp.PythonProcess.PipeOut.WriteString(code)
	if err != nil {
		return "", err
	}

	// we will receive a status or an exception first
	var exception *PythonException = nil
	var exerr error = nil
	select {
	case <-rpp.StatusChan:
		// Status received, continue
	case e := <-rpp.ExceptionChan:
		exception = e
	}

	if exception != nil {
		exerr = exception.Error()
	}

	// Read the output from Python and process it until we encounter the delimiter
	reader := bufio.NewReader(rpp.PythonProcess.PipeIn)
	var result strings.Builder

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}

		result.WriteString(line)

		if iswin {
			// Check if we've received the complete output (marked by the WINDELIMITER)
			if strings.HasSuffix(result.String(), WINDELIMITER) {
				// Trim the delimiter and any trailing newline/carriage return from the output
				output := strings.TrimSuffix(result.String(), WINDELIMITER)
				output = strings.TrimRight(output, "\n\r")
				return output, exerr
			}
		} else {
			// Check if we've received the complete output (marked by the delimiter)
			if strings.HasSuffix(result.String(), DELIMITER) {
				// Trim the delimiter and any trailing newline/carriage return from the output
				output := strings.TrimSuffix(result.String(), DELIMITER)
				output = strings.TrimRight(output, "\n\r")
				return output, exerr
			}
		}

		if err == io.EOF {
			return "", fmt.Errorf("unexpected EOF")
		}
	}
}

// ExecuteWithTimeout runs Python code with a maximum execution time.
//
// Parameters:
//   - code: Python source code to execute
//   - combinedOutput: If true, stdout and stderr are merged
//   - timeout: Maximum time to wait for completion
//
// If the timeout is exceeded, the Python process is terminated and the REPL
// is marked as closed. Subsequent calls will return an error.
//
// Note: After a timeout, the REPL cannot be reused. Create a new one if needed.
func (rpp *REPLPythonProcess) ExecuteWithTimeout(code string, combinedOutput bool, timeout time.Duration) (string, error) {
	// we need to lock the mutex to prevent multiple goroutines from writing to the Python process at the same time
	rpp.m.Lock()
	defer rpp.m.Unlock()

	// check if the Python process has been closed
	if rpp.closed {
		return "", fmt.Errorf("REPL process has been closed")
	}

	// if we are changing the combined output setting, update the Python process
	if rpp.combinedOutput != combinedOutput {
		cc := "__CAPTURE_COMBINED__ ="
		if combinedOutput {
			cc += " True" + DELIMITER
		} else {
			cc += " False" + DELIMITER
		}
		_, err := rpp.PythonProcess.PipeOut.WriteString(cc)
		if err != nil {
			return "", err
		}
		rpp.combinedOutput = combinedOutput
	}

	// trim whitespace from the end of the code
	code = strings.TrimRight(code, " \t\n\r")

	// append the DELIMITER to the end of the code
	code += DELIMITER

	// write the code to the Python process as a single string
	_, err := rpp.PythonProcess.PipeOut.WriteString(code)
	if err != nil {
		return "", err
	}

	// Create a channel to receive the result
	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)

	// Start a goroutine to read from the Python process
	go func() {
		reader := bufio.NewReader(rpp.PythonProcess.PipeIn)
		var result strings.Builder

		for {
			line, err := reader.ReadString('\n')
			if err != nil && err != io.EOF {
				errCh <- err
				return
			}

			result.WriteString(line)

			// Check if we've received the complete output (marked by the delimiter)
			if strings.HasSuffix(result.String(), DELIMITER) {
				// Trim the delimiter and any trailing newline/carriage return from the output
				output := strings.TrimSuffix(result.String(), DELIMITER)
				output = strings.TrimRight(output, "\n\r")
				resultCh <- output
				return
			}

			if err == io.EOF {
				errCh <- fmt.Errorf("unexpected EOF")
				return
			}
		}
	}()

	// Use select to wait for either the result, error, or a timeout
	select {
	case output := <-resultCh:
		return output, nil
	case err := <-errCh:
		return "", err
	case <-time.After(timeout):
		// If the timeout is reached, we can't wait for the Python process to finish
		// so we need to terminate it and mark it as closed
		rpp.PythonProcess.Terminate()
		rpp.closed = true
		return "", fmt.Errorf("execution timed out - Python process terminated")
	}
}

// Close terminates the Python REPL process and releases resources.
// After Close, the REPL cannot be reused. Returns an error if already closed.
func (rpp *REPLPythonProcess) Close() error {
	// we need to lock the mutex to prevent multiple goroutines from writing to the Python process at the same time
	rpp.m.Lock()
	defer rpp.m.Unlock()

	// check if the Python process has been closed
	if rpp.closed {
		return fmt.Errorf("REPL process has been closed")
	}
	rpp.closed = true
	return rpp.PythonProcess.Terminate()
}
