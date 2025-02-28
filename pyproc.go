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
	"syscall"
	"text/template"
	"time"
)

// The file descriptor is passed as an extra file, so it will be after stderr
//
//go:embed scripts/bootstrap.py
var primaryBootstrapScriptTemplate string

//go:embed scripts/secondaryBootstrapScript.py
var secondaryBootstrapScriptTemplate string

//go:embed packages/jumpboot/*.py
var jumpboot_package embed.FS

// PythonProcess represents a running Python process with its I/O pipes
type PythonProcess struct {
	Cmd      *exec.Cmd
	Stdin    io.WriteCloser
	Stdout   io.ReadCloser
	Stderr   io.ReadCloser
	PipeIn   *os.File
	PipeOut  *os.File
	StatusIn *os.File
}

// Module represents a Python module
type Module struct {
	// Name of the module
	Name string
	// Path to the module
	Path string
	// Base64 encoded source code of the module
	Source string
}

// Package represents a Python package
type Package struct {
	// Name of the package
	Name string
	// Path to the package
	Path string
	// Modules in the package
	Modules []Module
	// Subpackages in the package
	Packages []Package
}

// PythonProgram represents a Python program with its main module and supporting packages and modules
type PythonProgram struct {
	Name     string
	Path     string
	Program  Module
	Packages []Package
	Modules  []Module
	PipeIn   int
	PipeOut  int
	StatusIn int
	// DebugPort - setting this to a non-zero value will start the debugpy server on the specified port
	// and wait for the debugger to attach before running the program in the bootstrap script
	DebugPort    int
	BreakOnStart bool
	KVPairs      map[string]interface{}
}

// Data struct to hold the pipe number
type TemplateData struct {
	PipeNumber int
}

// NewModuleFromPath creates a new module from a file path
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

// NewModuleFromString creates a new module from a string
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

// NewPackage creates a new package from a collection of modules
func NewPackage(name, path string, modules []Module) *Package {
	return &Package{
		Name:    name,
		Path:    path,
		Modules: modules,
	}
}

// fsDirHasInitPy returns true if the fs directory contains a __init__.py file
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

// New Package from an embed.FS containing the package structure and source files
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

func (env *Environment) NewPythonProcessFromProgram(program *PythonProgram, environment_vars map[string]string, extrafiles []*os.File, debug bool, args ...string) (*PythonProcess, []byte, error) {
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
				continue
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

	return pyProcess, programData, nil
}

// NewPythonProcessFromString starts a Python script from a string with the given arguments.
// It returns a PythonProcess struct containing the command and I/O pipes.
// It ensures that the child process is killed if the parent process is killed.
func (env *Environment) NewPythonProcessFromString(script string, environment_vars map[string]string, extrafiles []*os.File, debug bool, args ...string) (*PythonProcess, error) {
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

// Wait waits for the Python process to exit and returns an error if it was killed
func (pp *PythonProcess) Wait() error {
	err := pp.Cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == -1 {
				// The child process was killed
				return errors.New("child process was killed")
			}
		}
		return err
	}
	return nil
}

// Terminate gracefully stops the Python process
func (pp *PythonProcess) Terminate() error {
	if pp.Cmd.Process == nil {
		return nil // Process hasn't started or has already finished
	}

	// Try to terminate gracefully first
	err := pp.Cmd.Process.Signal(syscall.SIGTERM)
	if err != nil {
		return err
	}

	// Wait for the process to exit
	done := make(chan error, 1)
	go func() {
		done <- pp.Cmd.Wait()
	}()

	// Wait for the process to exit or force kill after timeout
	select {
	case <-time.After(5 * time.Second):
		// Force kill if it doesn't exit within 5 seconds
		err = pp.Cmd.Process.Kill()
		if err != nil {
			return err
		}
		<-done // Wait for the process to be killed
	case err = <-done:
		// Process exited before timeout
	}

	return err
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
