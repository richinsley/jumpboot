package jumpboot

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
)

// RunPythonReadCombined runs a Python script and returns the combined standard output and standard error.
// RunPythonReadCombined blocks until the child process exits.
func (env *Environment) RunPythonReadCombined(scriptPath string, args ...string) (string, error) {
	args = append([]string{scriptPath}, args...)
	cmd := exec.Command(env.PythonPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), err
	}
	return string(output), nil
}

// RunPythonReadStdout runs a Python script and returns the standard output.
// RunPythonReadStdout blocks until the child process exits.
func (env *Environment) RunPythonReadStdout(scriptPath string, args ...string) (string, error) {
	// put scriptPath at the front of the args
	retv := ""
	args = append([]string{scriptPath}, args...)
	cmd := exec.Command(env.PythonPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	defer stdout.Close()

	// continue to read the output until there is no more
	// or an error occurs
	if err := cmd.Start(); err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		retv += scanner.Text() + "\n"
	}
	return retv, nil
}

// RunPythonScriptFromFile runs a Python script from a file with the given arguments.
// RunPythonScriptFromFile blocks until the child process exits.
func (env *Environment) RunPythonScriptFromFile(scriptPath string, args ...string) error {
	// put scriptPath at the front of the args
	args = append([]string{scriptPath}, args...)
	cmd := exec.Command(env.PythonPath, args...)

	// Create a pipe for the output of the script
	stdoutPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("error creating stdout pipe: %v", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return err
	}

	// Read from the command's stdout
	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		fmt.Println("Python script output:", scanner.Text())
	}

	// Wait for the command to finish
	if err := cmd.Wait(); err != nil {
		return err
	}

	return nil
}

// BoundRunPythonScriptFromFile runs a Python script from a file with the given arguments.
// It ensures that the child process is terminated if the parent process is killed.
// BoundRunPythonScriptFromFile blocks until the child process exits.  This is the most
// general function for running a Python program as a child process.
func (env *Environment) BoundRunPythonScriptFromFile(scriptPath string, args ...string) error {
	// Create the command
	// put scriptPath at the front of the args
	args = append([]string{scriptPath}, args...)
	cmd := exec.Command(env.PythonPath, args...)

	// Create a pipe for the output of the script
	stdoutPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return err
	}

	// Create a channel to receive signals
	signalChan := make(chan os.Signal, 1)
	setSignalsForChannel(signalChan)

	// Wait for the command to finish or a signal to be received
	go func() {
		<-signalChan
		// Kill the child process when a signal is received
		cmd.Process.Kill()
	}()

	// Read from the command's stdout
	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		fmt.Println("Python script output:", scanner.Text())
	}

	// Wait for the command to finish
	return waitForExit(cmd)
}
