package main

import (
	"bufio"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

var main_program string = `
import jumpboot
import os
import sys
from multiprocessing import shared_memory

# we stored the shared memory name and size in the environment and the semaphore name
# in the jumpboot module, so we can access them here
name = jumpboot.SHARED_MEMORY_NAME
size = jumpboot.SHARED_MEMORY_SIZE
semname = jumpboot.SEMAPHORE_NAME

# open the named semaphore
sem = jumpboot.NamedSemaphore(semname)

try:
	# acquire the semaphore to know when the shared memory is ready
	print("Trying to acquire semaphore...")
	sem.acquire()

	print("Releasing semaphore")
	sem.release()
finally:
	sem.close()

# open the shared memory segment and read the data
try:
	# Attach to the existing shared memory segment
	shm = shared_memory.SharedMemory(name=name, create=False, size=size)
	
	# Read data from the shared memory
	data = shm.buf[:].tobytes()
	print(f"Data read from shared memory: {data.decode('utf-8')}")

	# Close the shared memory segment
	shm.close()
except Exception as e:
	print(f"Error: {e}")

print("exit")
`

func main() {
	// Specify the binary folder to place micromamba in
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	fmt.Println("Creating Jumpboot repo at: ", rootDirectory)

	progressFunc := func(message string, current, total int64) {
		if total > 0 {
			fmt.Printf("\r%s: %.2f%%\n", message, float64(current)/float64(total)*100)
		} else {
			fmt.Printf("\r%s: %d\n", message, current)
		}
	}

	// baseEnv, err := jumpboot.CreateEnvironmentFromSystem(progressFunc)
	version := "3.11"
	baseEnv, err := jumpboot.CreateEnvironmentMamba("myenv"+version, rootDirectory, version, "conda-forge", progressFunc)
	if err != nil {
		log.Fatalf("Error creating environment: %v", err)
	}
	fmt.Printf("Created environment: %s\n", baseEnv.Name)

	if baseEnv.IsNew {
		fmt.Println("Created a new environment")
	}

	// create a virtual environment from the system python
	venvOptions := jumpboot.VenvOptions{
		SystemSitePackages: true,
		Upgrade:            true,
		Prompt:             "my-venv",
		UpgradeDeps:        true,
	}
	env, err := jumpboot.CreateVenvEnvironment(baseEnv, path.Join(rootDirectory, "sysvenv"), venvOptions, progressFunc)
	if err != nil {
		log.Fatalf("Error creating venv environment: %v", err)
	}

	if env.IsNew {
		fmt.Println("Created a new venv environment")
		env.PipInstallPackages([]string{"numba", "numpy"}, "", "", false, nil)
	}

	// create a shared semaphore
	semaphore_name := "/MySemaphore"
	if runtime.GOOS == "windows" {
		semaphore_name = "MySemaphore"
	}
	sem, err := jumpboot.NewSemaphore(semaphore_name, 0)
	if err != nil {
		log.Fatalf("Failed to create semaphore: %v", err)
	}

	// Create a shared memory segment
	name := "my_shared_memory"
	size := 1024
	segment, err := jumpboot.CreateSharedMemory(name, size)
	if err != nil {
		log.Fatalf("Failed to create shared memory: %v", err)
	}
	defer segment.Close()

	// Write some data to the shared memory
	data := []byte("Hello from Go!")
	_, err = segment.Write(data)
	if err != nil {
		log.Fatalf("Failed to write to shared memory: %v", err)
	}

	// Get the shared memory name and size

	program := &jumpboot.PythonProgram{
		Name: "MyProgram",
		Path: cwd,
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   path.Join(cwd, "modules", "main.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(main_program)),
		},
		Modules:  []jumpboot.Module{},
		Packages: []jumpboot.Package{},
		KVPairs:  map[string]interface{}{"SHARED_MEMORY_NAME": name, "SHARED_MEMORY_SIZE": size, "SEMAPHORE_NAME": semaphore_name},
	}

	// create a string map of env options to pass to the Python process
	envOptions := map[string]string{}

	pyProcess, _, err := env.NewPythonProcessFromProgram(program, envOptions, nil, false)
	if err != nil {
		panic(err)
	}

	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	err = sem.Release()
	if err != nil {
		log.Fatalf("Failed to acquire semaphore: %v", err)
	}

	// Read the output line by line until we see the exit command
	scanner := bufio.NewScanner(pyProcess.Stdout)
	var output string
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println(line)     // Print each line (optional)
		output += line + "\n" // Store the line in the output string
		if strings.HasPrefix(line, "exit") {
			break
		}
	}

	// Wait for the Python process to finish
	pyProcess.Cmd.Wait()
}
