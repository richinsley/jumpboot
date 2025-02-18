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
	"strings"

	jumpboot "github.com/richinsley/jumpboot"
)

//go:embed modules/gradio_app.py
var gradioScript string

// --- Go Functions ---
// These functions are now exported (start with capital letter)
// so they can be called by the command handler.

func Add(x, y float64) float64 {
	return x + y
}

func Greet(name string) string {
	return "Hello, " + name + " from Go!"
}

func Uppercase(text string) string {
	return strings.ToUpper(text)
}

func Reverse(numbers []float64) []float64 {
	for i, j := 0, len(numbers)-1; i < j; i, j = i+1, j-1 {
		numbers[i], numbers[j] = numbers[j], numbers[i]
	}
	return numbers
}

func main() {
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	version := "3.12"

	fmt.Println("Creating jumpboot Python environment")
	env, err := jumpboot.CreateEnvironmentMamba("gradio_env", rootDirectory, version, "conda-forge", nil)
	if err != nil {
		log.Fatalf("Failed to create environment: %v", err)
	}

	if env.IsNew {
		fmt.Println("Installing Gradio package")
		err = env.PipInstallPackage("gradio", "", "", false, nil)
		if err != nil {
			log.Fatalf("Failed to install gradio: %v", err)
		}
	}

	program := &jumpboot.PythonProgram{
		Name: "GradioApp",
		Path: cwd,
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   filepath.Join(cwd, "modules", "gradio_app.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(gradioScript)),
		},
	}

	pyProcess, _, err := env.NewPythonProcessFromProgram(program, nil, nil, false)
	if err != nil {
		log.Fatalf("Failed to start Python process: %v", err)
	}
	defer pyProcess.Terminate()

	// Goroutine to read Python's stdout
	go func() {
		io.Copy(os.Stdout, pyProcess.Stdout)
	}()

	// Goroutine to read Python's stderr
	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	reader := bufio.NewReader(pyProcess.PipeIn)
	writer := pyProcess.PipeOut

	// Function Map: Command Name -> Go Function
	funcMap := map[string]interface{}{
		"add":       Add,
		"greet":     Greet,
		"uppercase": Uppercase,
		"reverse":   Reverse,
	}

	// We do *not* send a "start_gui" command anymore. The Python script
	// will start the Gradio app directly.

	// Message Handling Loop (in a goroutine)
	go func() {
		for {
			requestJSON, err := reader.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					log.Println("Python process closed the pipe.")
					break
				}
				log.Printf("Error reading from Python: %v", err)
				return // Exit goroutine on error
			}

			var request map[string]interface{}
			if err := json.Unmarshal(requestJSON, &request); err != nil {
				log.Printf("Error decoding JSON request: %v, data: %s", err, string(requestJSON))
				continue
			}

			command, ok := request["command"].(string)
			if !ok {
				log.Println("Invalid command format")
				continue
			}

			// Check for the exit command *before* looking up the function.
			if command == "exit" {
				fmt.Println("Received exit command, exiting.")
				break // Exit the message handling loop.
			}
			//get the request ID
			requestID, hasRequestID := request["request_id"].(string)

			data, ok := request["data"]
			if !ok && command != "exit" {
				log.Println("Invalid data format")
				continue
			}

			f, found := funcMap[command]
			if !found {
				log.Printf("Unknown command: %s", command)
				response := map[string]interface{}{"error": fmt.Sprintf("Unknown command: %s", command)}
				if hasRequestID { // Always include request_id if present
					response["request_id"] = requestID
				}
				responseJSON, _ := json.Marshal(response)
				_, err = writer.Write(append(responseJSON, '\n'))
				if err != nil {
					log.Printf("Failed to write to Python: %v", err)
				}
				continue
			}

			var result interface{}
			var callErr error

			// Call the Go function based on its signature (using type assertions).
			switch f.(type) {
			case func(float64, float64) float64:
				args, _ := data.(map[string]interface{})
				x, _ := args["x"].(float64)
				y, _ := args["y"].(float64)
				result = f.(func(float64, float64) float64)(x, y)
			case func(string) string:
				arg, _ := data.(string)
				result = f.(func(string) string)(arg)
			case func([]float64) []float64:
				arg_i, ok := data.([]interface{}) //list of interfaces
				if !ok {
					log.Println("Invalid arguments for reverse")
					continue
				}
				//convert list of interfaces to list of float64
				arg := make([]float64, len(arg_i))
				for i, v := range arg_i {
					arg[i], ok = v.(float64)
					if !ok {
						log.Println("Invalid argument for reverse")
						continue
					}
				}
				result = f.(func([]float64) []float64)(arg)

			default:
				callErr = fmt.Errorf("unsupported function type for command: %s", command)
			}

			// Send response (result or error).
			response := make(map[string]interface{})
			if hasRequestID { // Always include request_id if it was sent
				response["request_id"] = requestID
			}

			if callErr != nil {
				response["error"] = callErr.Error()
			} else {
				response["result"] = result
			}

			responseJSON, _ := json.Marshal(response)
			_, err = writer.Write(append(responseJSON, '\n'))
			if err != nil {
				log.Printf("Error writing response to Python: %v", err)
				return
			}
		}
	}()
	// Use a channel to signal when to exit.
	exitChan := make(chan struct{})
	go func() {
		//Simulate doing work by waiting for user input.
		fmt.Println("Press Enter to exit...")
		fmt.Scanln()
		close(exitChan)
	}()

	select {
	case <-exitChan:
		fmt.Println("Exiting...")
	}
}
