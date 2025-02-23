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

	jumpboot "github.com/richinsley/jumpboot"
)

//go:embed modules/chromadb_app.py
var chromadbScript string

func main() {
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	version := "3.12"

	fmt.Println("Creating Jumpboot env at: ", rootDirectory)
	env, err := jumpboot.CreateEnvironmentMamba("chromadb_env", rootDirectory, version, "conda-forge", nil)
	if err != nil {
		log.Fatalf("Failed to create environment: %v", err)
	}

	if env.IsNew {
		fmt.Println("Installing chromadb via pip...")
		// chromadb requires several packages that must be pip installed:
		//  chromadb
		//  tiktoken
		//  hnswlib
		//  pydantic
		//  posthog
		//  pulsar-client
		//  overrides
		//  fastapi
		//  uvicorn
		err = env.PipInstallPackages([]string{"chromadb", "tiktoken"}, "", "", false, nil)
		if err != nil {
			log.Fatalf("Failed to install chromadb: %v", err)
		}
	}

	program := &jumpboot.PythonProgram{
		Name: "ChromaDBApp",
		Path: cwd,
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   filepath.Join(cwd, "modules", "chromadb_app.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(chromadbScript)),
		},
	}

	pyProcess, _, err := env.NewPythonProcessFromProgram(program, nil, nil, false)
	if err != nil {
		log.Fatalf("Failed to start Python process: %v", err)
	}
	defer pyProcess.Terminate()

	// Goroutines for stdout and stderr
	go func() {
		io.Copy(os.Stdout, pyProcess.Stdout)
	}()
	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	reader := bufio.NewReader(pyProcess.PipeIn)
	writer := pyProcess.PipeOut

	// Helper function for sending commands and receiving responses
	sendCommand := func(command string, data interface{}) (map[string]interface{}, error) {
		request := map[string]interface{}{
			"command": command,
			"data":    data,
		}
		requestJSON, _ := json.Marshal(request)
		_, err := writer.Write(append(requestJSON, '\n')) // Crucial newline
		if err != nil {
			return nil, fmt.Errorf("failed to write to Python: %w", err)
		}

		responseJSON, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read from Python: %w", err)
		}

		var response map[string]interface{}
		if err := json.Unmarshal(responseJSON, &response); err != nil {
			return nil, fmt.Errorf("failed to decode JSON response: %w", err)
		}
		return response, nil
	}

	// --- Interaction with ChromaDB ---
	fmt.Println("Creating collection...")
	_, err = sendCommand("create_collection", map[string]string{"name": "my_collection"})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Adding documents...")
	_, err = sendCommand("add_documents", map[string]interface{}{
		"collection_name": "my_collection",
		"documents": []string{
			"This is document 1",
			"This is document 2",
			"This is document 3",
		},
		"metadatas": []map[string]string{
			{"source": "doc1"},
			{"source": "doc2"},
			{"source": "doc3"},
		},
		"ids": []string{"id1", "id2", "id3"},
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Performing query...")
	response, err := sendCommand("query", map[string]interface{}{
		"collection_name": "my_collection",
		"query_texts":     []string{"document"},
		"n_results":       2,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Check for error response from Python
	if errMsg, ok := response["error"].(string); ok {
		log.Fatalf("Error from Python: %s", errMsg)
	}

	fmt.Printf("Query results: %v\n", response)

	// Demonstrate getting documents by ID
	fmt.Println("Getting documents by ID...")
	getResponse, err := sendCommand("get", map[string]interface{}{
		"collection_name": "my_collection",
		"ids":             []string{"id1", "id3"},
	})
	if err != nil {
		log.Fatal(err)
	}

	if errMsg, ok := getResponse["error"].(string); ok {
		log.Fatalf("Error from Python: %s", errMsg)
	}
	fmt.Printf("Get results: %v\n", getResponse)

	// Send exit command
	_, err = sendCommand("exit", nil)
	if err != nil {
		log.Println("Error sending exit:", err) // Don't fatal, just log
	}

	pyProcess.Wait() //  Wait for process to exit cleanly
	fmt.Println("Python process exited.")
}
