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

// REPLPythonProcess represents a Python process that can execute code in a REPL-like manner
type REPLPythonProcess struct {
	*PythonProcess
	m              sync.Mutex
	closed         bool
	combinedOutput bool
}

// NewREPLPythonProcess creates a new Python process that can execute code in a REPL-like manner
// kvpairs parameter is a map of key-value pairs to pass to the Python process that are accessible in the Python code via the jumpboot module.
// environment_vars parameter is a map of environment variables to set in the Python process.
func (env *Environment) NewREPLPythonProcess(kvpairs map[string]interface{}, environment_vars map[string]string, modules []Module, packages []Package) (*REPLPythonProcess, error) {
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

// Define the custom delimiter with non-visible ASCII characters
const DELIMITER = "\x01\x02\x03\n"

// because of course Windows has to be different, we need a different read delimiter for Windows
// however, we can use the same write delimiter because the Python process will always use the same delimiter
const WINDELIMITER = "\x01\x02\x03\r\n"

// Execute executes the given code in the Python process and returns the output.
// code parameter is the Python code to execute within the REPLPythonProcess.
// combinedOutput parameter specifies whether to combine stdout and stderr as the result.
// Execute is a blocking function that waits for the Python process to finish executing the code.
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
	case s := <-rpp.StatusChan:
		fmt.Println("received", s)
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

// ExecuteWithTimeout executes the given code in the Python process and returns the output.
// code parameter is the Python code to execute within the REPLPythonProcess.
// combinedOutput parameter specifies whether to combine stdout and stderr as the result.
// timeout parameter specifies the maximum time to wait for the Python process to finish executing the code.
// ExecuteWithTimeout is a non-blocking function that waits for the Python process to finish executing the code up to the specified timeout.
// If the timeout is reached, the Python process is terminated, REPLPythonProcess is marked as closed, and an error is returned.
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

// Close closes the Python REPL process.
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
