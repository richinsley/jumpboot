package jumpboot

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"strconv"
	"sync"
	"syscall"
	"text/template"
	"time"
)

// ErrProcessExited is returned when an operation is attempted on a process that has exited.
var ErrProcessExited = errors.New("python process has exited")

//go:embed scripts/bootstrap.py
var primaryBootstrapScriptTemplate string

//go:embed scripts/secondaryBootstrapScript.py
var secondaryBootstrapScriptTemplate string

//go:embed packages/jumpboot/* packages/jumpboot/**/*
var jumpboot_package embed.FS

// ProcOnException is a callback function invoked when a Python exception occurs.
type ProcOnException func(ex PythonException)

// ProcStatus is a callback function invoked when the Python process sends a status message.
type ProcStatus func(status string)

// ProcessExitHandler is called when a Python process exits unexpectedly.
// The exitErr contains the process exit information (nil if clean exit).
type ProcessExitHandler func(exitErr error)

// PythonProcess represents a running Python subprocess with communication pipes.
//
// The process uses a two-stage bootstrap mechanism: a primary bootstrap script
// initializes file descriptors and executes the secondary bootstrap, which sets
// up the custom import system and runs the main program.
//
// Communication occurs through multiple channels:
//   - Stdin/Stdout/Stderr: Standard I/O streams
//   - PipeIn/PipeOut: Primary data communication pipes
//   - StatusIn: Status and exception reporting from Python
type PythonProcess struct {
	// Cmd is the underlying exec.Cmd for the Python process.
	Cmd *exec.Cmd

	// Stdin is the write end of the process's standard input.
	Stdin io.WriteCloser

	// Stdout is the read end of the process's standard output.
	Stdout io.ReadCloser

	// Stderr is the read end of the process's standard error.
	Stderr io.ReadCloser

	// PipeIn is for reading data sent from the Python process.
	PipeIn *os.File

	// PipeOut is for writing data to the Python process.
	PipeOut *os.File

	// StatusIn receives status messages and exceptions from Python.
	StatusIn *os.File

	// ExceptionChan receives Python exceptions reported via the status pipe.
	ExceptionChan chan *PythonException

	// StatusChan receives status messages (e.g., "exit") from Python.
	StatusChan chan map[string]interface{}

	// OnExit is called when the process exits. Set before starting the process
	// monitor, or before calling MonitorProcess().
	OnExit ProcessExitHandler

	// exitOnce ensures the exit handler is called at most once.
	exitOnce sync.Once

	// exited indicates whether the process has exited.
	exited bool

	// exitErr stores the error from process exit (nil for clean exit).
	exitErr error

	// exitMu protects exited and exitErr.
	exitMu sync.RWMutex

	// exitChan is closed when the process exits, allowing waiters to unblock.
	exitChan chan struct{}
}

// Module represents a Python module that can be embedded in a Go binary.
// The source code is stored as base64-encoded text and decoded by the
// Python bootstrap script before execution.
type Module struct {
	// Name is the module name as it appears in Python imports (e.g., "utils").
	Name string

	// Path is the virtual file path used for __file__ and tracebacks.
	Path string

	// Source is the base64-encoded Python source code.
	Source string
}

// Package represents a Python package (directory with __init__.py) that can be
// embedded in a Go binary. Packages can contain modules and nested subpackages.
type Package struct {
	// Name is the package name as it appears in Python imports.
	Name string

	// Path is the virtual directory path for the package.
	Path string

	// Modules contains the Python modules in this package.
	Modules []Module

	// Packages contains nested subpackages.
	Packages []Package
}

// PythonProgram defines a complete Python program to be executed, including
// the main module, supporting packages, modules, and configuration options.
//
// The program is serialized to JSON and passed to the Python bootstrap script,
// which reconstructs the module hierarchy and executes the main program.
type PythonProgram struct {
	// Name identifies the program (used for logging and debugging).
	Name string

	// Path is the base path for resolving relative imports.
	Path string

	// Program is the main module (__main__) to execute.
	Program Module

	// Packages contains Python packages available for import.
	Packages []Package

	// Modules contains standalone Python modules available for import.
	Modules []Module

	// PipeIn is the file descriptor number for reading from Go (set automatically).
	PipeIn int

	// PipeOut is the file descriptor number for writing to Go (set automatically).
	PipeOut int

	// StatusIn is the file descriptor for status/exception reporting (set automatically).
	StatusIn int

	// DebugPort, if non-zero, starts debugpy on this port and waits for attachment.
	DebugPort int

	// BreakOnStart, if true with DebugPort set, breaks at the first line of code.
	BreakOnStart bool

	// KVPairs contains key-value data accessible in Python as jumpboot.<key>.
	KVPairs map[string]interface{}
}

// TemplateData holds data for rendering the bootstrap script templates.
type TemplateData struct {
	// PipeNumber is the file descriptor number for the bootstrap pipe.
	PipeNumber int
}

// NewModuleFromPath creates a Module by reading Python source from a file.
// The source is automatically base64-encoded for embedding.
//
// Parameters:
//   - name: The module name for Python imports
//   - path: The filesystem path to the .py file
func NewModuleFromPath(name, path string) (*Module, error) {
	// load the source file from the path
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// base64 encode the source
	encoded := base64.StdEncoding.EncodeToString(source)

	return &Module{
		Name:   name,
		Path:   path,
		Source: encoded,
	}, nil
}

// NewModuleFromString creates a Module from Python source code provided as a string.
// The source is automatically base64-encoded for embedding.
//
// Parameters:
//   - name: The module name for Python imports
//   - original_path: The virtual path used for __file__ in Python
//   - source: The Python source code as a plain string
func NewModuleFromString(name, original_path string, source string) *Module {
	// Trim the "packages/" prefix if it exists
	path := original_path
	// if filepath.HasPrefix(path, "packages/") {
	// 	path = filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path))
	// }

	// base64 encode the source
	encoded := base64.StdEncoding.EncodeToString([]byte(source))

	return &Module{
		Name:   name,
		Source: encoded,
		Path:   path,
	}
}

// NewPackage creates a Package from a collection of already-created modules.
// For loading packages from the filesystem, use NewPackageFromFS instead.
func NewPackage(name, path string, modules []Module) *Package {
	return &Package{
		Name:    name,
		Path:    path,
		Modules: modules,
	}
}

// fsDirHasInitPy checks if a directory in an embed.FS contains __init__.py,
// indicating it's a valid Python package.
func fsDirHasInitPy(fs embed.FS, path string) bool {
	// read the directory.  If the directory contains a __init__.py file, then it is a package
	entries, err := fs.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() == "__init__.py" {
			return true
		}
	}
	return false
}

func newPackageFromFS(name string, sourcepath string, rootpath string, fs embed.FS) (*Package, error) {
	retv := &Package{
		Name: name,
		Path: rootpath,
	}

	entries, err := fs.ReadDir(rootpath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		fpath := path.Join(rootpath, entry.Name())
		if entry.IsDir() {
			subpackage, err := newPackageFromFS(entry.Name(), sourcepath, fpath, fs)
			if err != nil {
				continue
			}
			retv.Packages = append(retv.Packages, *subpackage)
		} else {
			// Use the fpath directly, which now uses forward slashes
			file, err := fs.Open(fpath)
			if err != nil {
				return nil, err
			}
			defer file.Close()

			source, err := io.ReadAll(file)
			if err != nil {
				return nil, err
			}

			if path.Ext(entry.Name()) != ".py" {
				continue
			} else {
				module := NewModuleFromString(entry.Name(), fpath, string(source))
				retv.Modules = append(retv.Modules, *module)
			}
		}
	}

	return retv, nil
}

// NewPackageFromFS creates a Package by recursively loading Python files from an embed.FS.
// This is the recommended way to embed Python packages in Go binaries.
//
// Parameters:
//   - name: The package name for Python imports
//   - sourcepath: The source identifier (used internally)
//   - rootpath: The path within the embed.FS to the package root
//   - fs: The embedded filesystem containing the Python package
//
// Example:
//
//	//go:embed packages/mypackage/*
//	var myPackageFS embed.FS
//
//	pkg, err := jumpboot.NewPackageFromFS("mypackage", "mypackage", "packages/mypackage", myPackageFS)
func NewPackageFromFS(name string, sourcepath string, rootpath string, fs embed.FS) (*Package, error) {
	// the embedded filesystem should be a directory

	return newPackageFromFS(name, sourcepath, rootpath, fs)
}

func procTemplate(templateStr string, data interface{}) string {
	// Parse the template
	tmpl, err := template.New("pythonTemplate").Parse(templateStr)
	if err != nil {
		log.Fatalf("Error parsing template: %v", err)
	}

	// Execute the template with the data
	var result bytes.Buffer
	err = tmpl.Execute(&result, data)
	if err != nil {
		log.Fatalf("Error executing template: %v", err)
	}

	return result.String()
}

// NewPythonProcessFromProgram starts a Python process running the specified program.
// This is the primary method for launching Python code with the full bootstrap mechanism.
//
// The function:
//  1. Prepends the jumpboot package to the program's packages
//  2. Creates communication pipes (data, status, bootstrap)
//  3. Starts Python with the primary bootstrap script
//  4. Sends the secondary bootstrap and program data
//  5. Sets up signal handling for clean shutdown
//
// Parameters:
//   - program: The PythonProgram to execute
//   - environment_vars: Additional environment variables for the process
//   - extrafiles: Additional file handles to pass to Python
//   - debug: Currently unused, reserved for future debugging features
//   - args: Command-line arguments passed to the Python program
//
// Returns the PythonProcess, the JSON-encoded program data, and any error.
func (env *PythonEnvironment) NewPythonProcessFromProgram(program *PythonProgram, environment_vars map[string]string, extrafiles []*os.File, debug bool, args ...string) (*PythonProcess, []byte, error) {
	// create the jumpboot package
	jumpboot_package, err := newPackageFromFS("jumpboot", "jumpboot", "packages/jumpboot", jumpboot_package)
	if err != nil {
		return nil, nil, err
	}

	// prepend the jumpboot package to the list of packages
	program.Packages = append([]Package{*jumpboot_package}, program.Packages...)

	// Create two pipes for the bootstrap and the program data
	// these are closed after the data is written
	reader_bootstrap, writer_bootstrap, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	reader_program, writer_program, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}

	// Create two pipes for the primary input and output of the script
	// these are used to communicate with the primary bootstrap script
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

	// get the file descriptor for the bootstrap script
	reader_bootstrap_fd := reader_bootstrap.Fd()
	primaryBootstrapScript := procTemplate(primaryBootstrapScriptTemplate, TemplateData{PipeNumber: int(reader_bootstrap_fd)})

	// Create the command with the primary bootstrap script
	cmd := exec.Command(env.PythonPath)

	// Pass both file descriptors using ExtraFiles
	// this will return a list of strings with the file descriptors
	extradescriptors := setExtraFiles(cmd, append([]*os.File{pipein_writer_primary, pipeout_reader_primary, status_writer_primary, reader_bootstrap, reader_program}, extrafiles...))

	// truncate pipein_writer_primary, pipeout_reader_primary from extradescriptors
	// these are available as PipeIn and PipeOut in the PythonProgram struct
	program.PipeOut, _ = strconv.Atoi(extradescriptors[0])
	program.PipeIn, _ = strconv.Atoi(extradescriptors[1])
	program.StatusIn, _ = strconv.Atoi(extradescriptors[2])
	extradescriptors = extradescriptors[3:]

	// At this point, cmd.Args will contain just the python path.  We can now append the "-c" flag and the primary bootstrap script
	cmd.Args = append(cmd.Args, "-u", "-c", primaryBootstrapScript)

	// append the count of extra files to the command arguments as a string
	cmd.Args = append(cmd.Args, fmt.Sprintf("%d", len(extradescriptors)))

	// append the extra file descriptors to the command arguments
	cmd.Args = append(cmd.Args, extradescriptors...)

	// append the program arguments to the command arguments
	cmd.Args = append(cmd.Args, args...)

	// Set environment variables
	cmd.Env = os.Environ()
	for key, value := range environment_vars {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	// Create pipes for the input, output, and error of the script
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

	// Prepare the program data
	programData, err := json.Marshal(program)
	if err != nil {
		return nil, nil, err
	}

	// Prepare the status pipe
	schan := make(chan map[string]interface{}, 1)
	echan := make(chan *PythonException, 1)
	go func() {
		defer status_writer_primary.Close()
		statusScanner := bufio.NewScanner(status_reader_primary)
		for statusScanner.Scan() {
			var status map[string]interface{}
			text := statusScanner.Text()
			if err := json.Unmarshal([]byte(text), &status); err != nil {
				log.Printf("Error decoding status JSON request: %v, data: %s", err, string(text))
				break
			}
			if status["type"] == "status" {
				schan <- status
				if status["status"] == "exit" {
					break
				}
			} else if status["type"] == "exception" {
				exception, err := NewPythonExceptionFromJSON(statusScanner.Bytes())
				if err != nil {
					log.Printf("Error decoding Python exception: %v, %s", err, text)
					continue
				}
				log.Printf("Python exception: %s", exception.ToString())
				echan <- exception
				continue
			} else {
				log.Printf("Unknown status type: %s", text)
			}
		}
	}()

	// Start the command
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	// Write the secondary bootstrap script and program data to separate pipes
	go func() {
		defer writer_bootstrap.Close()
		secondaryBootstrapScript := procTemplate(secondaryBootstrapScriptTemplate, TemplateData{PipeNumber: int(reader_program.Fd())})
		io.WriteString(writer_bootstrap, secondaryBootstrapScript)
	}()

	go func() {
		defer writer_program.Close()
		writer_program.Write(programData)
	}()

	pyProcess := &PythonProcess{
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

	// Set up signal handling
	setupSignalHandler(pyProcess)

	return pyProcess, programData, nil
}

// NewPythonProcessFromString starts a Python process executing a script provided as a string.
// This is a simpler alternative to NewPythonProcessFromProgram for quick script execution.
//
// The script is passed via a pipe and executed using the primary bootstrap mechanism.
// Signal handling is configured to terminate the child if the parent is killed.
//
// Parameters:
//   - script: The Python source code to execute
//   - environment_vars: Additional environment variables for the process
//   - extrafiles: Additional file handles to pass to Python
//   - debug: Currently unused, reserved for future debugging features
//   - args: Command-line arguments accessible via sys.argv
func (env *PythonEnvironment) NewPythonProcessFromString(script string, environment_vars map[string]string, extrafiles []*os.File, debug bool, args ...string) (*PythonProcess, error) {
	// Create a pipe for the secondary bootstrap script
	// we'll write the script to the writer
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	// Create two pipes for the primary input and output of the script
	pipein_reader_primary, pipein_writer_primary, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	pipeout_reader_primary, pipeout_writer_primary, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	status_reader_primary, status_writer_primary, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	// Create the command with the bootstrap script
	// We want stdin/stdout to unbuffered (-u) and to run the bootstrap script
	// The "-c" flag is used to pass the script as an argument and terminates the python option list.
	bootloader := procTemplate(primaryBootstrapScriptTemplate, TemplateData{PipeNumber: int(reader.Fd())})
	fullArgs := append([]string{"-u", "-c", bootloader}, args...)
	cmd := exec.Command(env.PythonPath, fullArgs...)

	// Pass the file descriptor using ExtraFiles
	// prepend our reader to the list of extra files so it is always the first file descriptor
	extrafiles = append([]*os.File{reader, pipein_writer_primary, pipeout_reader_primary, status_writer_primary}, extrafiles...)
	setExtraFiles(cmd, extrafiles)

	// set it's environment variables as our environment variables
	cmd.Env = os.Environ()

	// set the environment variables if they are provided
	for key, value := range environment_vars {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	// Create pipes for the input, output, and error of the script
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Write the main script to the pipe
	go func() {
		// Close the writer when the function returns
		// Python will not run the bootstrap script until the writer is closed
		defer writer.Close()
		io.WriteString(writer, script)
	}()

	pyProcess := &PythonProcess{
		Cmd:      cmd,
		Stdin:    stdinPipe,
		Stdout:   stdoutPipe,
		Stderr:   stderrPipe,
		PipeIn:   pipein_reader_primary,
		PipeOut:  pipeout_writer_primary,
		StatusIn: status_reader_primary,
	}

	// Set up signal handling
	setupSignalHandler(pyProcess)

	return pyProcess, nil
}

// Wait blocks until the Python process exits.
// Returns an error if the process was killed or exited with a non-zero status.
// If MonitorProcess is active, Wait uses the cached exit state; otherwise it
// calls cmd.Wait() directly for backward compatibility.
func (pp *PythonProcess) Wait() error {
	if pp.exitChan != nil {
		// MonitorProcess is active, wait on its channel
		<-pp.exitChan
		return pp.ExitError()
	}
	// Fallback for direct PythonProcess usage without monitoring
	err := pp.Cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == -1 {
				return errors.New("child process was killed")
			}
		}
		return err
	}
	return nil
}

// Terminate gracefully stops the Python process by sending SIGTERM.
// If the process doesn't exit within 5 seconds, it is forcefully killed with SIGKILL.
// Returns nil if the process wasn't running or has already finished.
func (pp *PythonProcess) Terminate() error {
	if pp.Cmd.Process == nil {
		return nil // Process hasn't started or has already finished
	}

	// Already exited
	if !pp.Alive() {
		return pp.ExitError()
	}

	// Try to terminate gracefully first
	err := pp.Cmd.Process.Signal(syscall.SIGTERM)
	if err != nil {
		return err
	}

	// Use exitChan if monitor is active, otherwise spawn a waiter
	var done <-chan struct{}
	if pp.exitChan != nil {
		done = pp.exitChan
	} else {
		d := make(chan struct{}, 1)
		go func() {
			pp.Cmd.Wait()
			close(d)
		}()
		done = d
	}

	// Wait for the process to exit or force kill after timeout
	select {
	case <-time.After(5 * time.Second):
		// Force kill if it doesn't exit within 5 seconds
		err = pp.Cmd.Process.Kill()
		if err != nil {
			return err
		}
		<-done
	case <-done:
		// Process exited before timeout
	}

	return pp.ExitError()
}

// Alive returns true if the Python process is still running.
// This is a non-blocking check that reads cached state from the process monitor.
func (pp *PythonProcess) Alive() bool {
	pp.exitMu.RLock()
	defer pp.exitMu.RUnlock()
	return !pp.exited
}

// ExitError returns the error from process exit, or nil if the process
// exited cleanly or is still running. Use Alive() to distinguish between
// "still running" and "exited cleanly".
func (pp *PythonProcess) ExitError() error {
	pp.exitMu.RLock()
	defer pp.exitMu.RUnlock()
	return pp.exitErr
}

// ExitChan returns a channel that is closed when the process exits.
// This allows callers to select on process exit alongside other operations.
// Returns nil if MonitorProcess has not been called.
func (pp *PythonProcess) ExitChan() <-chan struct{} {
	return pp.exitChan
}

// MonitorProcess starts a background goroutine that waits for the process to exit
// and updates the process state. It calls OnExit (if set) when the process exits.
// This is called automatically by NewQueueProcess; call it manually only when using
// PythonProcess directly.
//
// Safe to call multiple times; only the first call starts monitoring.
func (pp *PythonProcess) MonitorProcess() {
	pp.exitOnce.Do(func() {
		pp.exitChan = make(chan struct{})
		go func() {
			err := pp.Cmd.Wait()
			pp.exitMu.Lock()
			pp.exited = true
			pp.exitErr = err
			pp.exitMu.Unlock()
			close(pp.exitChan)

			if pp.OnExit != nil {
				pp.OnExit(err)
			}
		}()
	})
}

func setupSignalHandler(pp *PythonProcess) {
	signalChan := make(chan os.Signal, 1)
	setSignalsForChannel(signalChan)

	go func() {
		<-signalChan
		// Terminate the child process when a signal is received
		pp.Terminate()
	}()
}
