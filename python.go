package jumpboot

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
)

// RunPythonReadCombined executes a Python script and returns combined stdout/stderr.
// This is a blocking call that waits for the script to complete.
func (env *PythonEnvironment) RunPythonReadCombined(scriptPath string, args ...string) (string, error) {
	args = append([]string{scriptPath}, args...)
	cmd := exec.Command(env.PythonPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), err
	}
	return string(output), nil
}

// RunPythonReadStdout executes a Python script and returns only stdout.
// This is a blocking call that waits for the script to complete.
func (env *PythonEnvironment) RunPythonReadStdout(scriptPath string, args ...string) (string, error) {
	return RunReadStdout(env.PythonPath, append([]string{scriptPath}, args...)...)
}

// RunPythonScriptFromFile executes a Python script file, printing stderr to stdout.
// This is a blocking call that waits for the script to complete.
func (env *PythonEnvironment) RunPythonScriptFromFile(scriptPath string, args ...string) error {
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

// BoundRunPythonScriptFromFile executes a Python script with proper signal handling.
// If the parent Go process receives a termination signal, the Python child is killed.
// This is the recommended way to run Python scripts as long-running subprocesses.
func (env *PythonEnvironment) BoundRunPythonScriptFromFile(scriptPath string, args ...string) error {
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
