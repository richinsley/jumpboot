package main

import (
	"bufio"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	jumpboot "github.com/richinsley/jumpboot"
)

//go:embed modules/command_response.py
var commandResponseScript string

func main() {
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	version := "3.12" // Use a specific version or "" for the latest

	env, err := jumpboot.CreateEnvironmentMamba("example1_env", rootDirectory, version, "conda-forge", nil)
	if err != nil {
		log.Fatalf("Failed to create environment: %v", err)
	}

	program := &jumpboot.PythonProgram{
		Name: "CommandResponse",
		Path: cwd,
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   filepath.Join(cwd, "modules", "command_response.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(commandResponseScript)),
		},
	}

	pyProcess, _, err := env.NewPythonProcessFromProgram(program, nil, nil, false)
	if err != nil {
		log.Fatalf("Failed to start Python process: %v", err)
	}
	defer pyProcess.Terminate() // Ensure process is terminated

	// Goroutine to read Python's stdout
	go func() {
		io.Copy(os.Stdout, pyProcess.Stdout)
	}()

	// Goroutine to read Python's stderr
	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	// Create a reader for the Python process's input pipe
	reader := bufio.NewReader(pyProcess.PipeIn)
	writer := pyProcess.PipeOut

	// Function to send a command and optionally receive a response
	sendCommand := func(command string, data interface{}, waitForResponse bool) (map[string]interface{}, error) {
		request := map[string]interface{}{
			"command": command,
			"data":    data,
		}
		requestJSON, _ := json.Marshal(request)
		_, err := writer.Write(append(requestJSON, '\n')) // Add newline for readline()
		if err != nil {
			return nil, fmt.Errorf("failed to write to Python: %w", err)
		}

		if !waitForResponse {
			return nil, nil // Return immediately if we don't need a response
		}

		responseJSON, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read from Python: %w", err)
		}

		var response map[string]interface{}
		if err := json.Unmarshal(responseJSON, &response); err != nil {
			return nil, fmt.Errorf("failed to decode JSON response: %w, response: %s", err, string(responseJSON))
		}
		return response, nil
	}

	// --- Example Usage ---

	// 1. Send a "greet" command (wait for response)
	response, err := sendCommand("greet", map[string]string{"name": "Go Program"}, true)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Greeting response: %v\n", response["message"])

	// 2. Send a "calculate" command (wait for response)
	result, err := sendCommand("calculate", map[string]int{"x": 5, "y": 7}, true)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Calculation result: %v\n", result["result"])

	// 3. Send an "exit" command (DO NOT wait for response)
	_, err = sendCommand("exit", nil, false) // No response expected
	if err != nil {
		log.Printf("Error sending exit command: %v", err) // Log, but don't fatal
	}

	// 4.  Send a command, but introduce a delay on the Python side so we can test
	// the timeout.
	go func() {
		_, err = sendCommand("long_running", nil, true) // Wait for a response
		if err != nil {
			fmt.Printf("Long running command returned error: %v\n", err)
		}
	}()

	time.Sleep(1 * time.Second) // Wait a short time before sending cancel.

	// 5. Send a "cancel" command (don't wait for a response)
	_, err = sendCommand("cancel", nil, false) // No response expected
	if err != nil {
		log.Printf("Error sending cancel command: %v", err)
	}

	pyProcess.Wait() // Important to wait for the process to finish
	fmt.Println("Python process exited.")
}
