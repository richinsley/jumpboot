# Jumpboot: Seamless Python Environment Management for Go

[![Go Reference](https://pkg.go.dev/badge/github.com/richinsley/jumpboot.svg)](https://pkg.go.dev/github.com/richinsley/jumpboot)
[![Go Report Card](https://goreportcard.com/badge/github.com/richinsley/jumpboot)](https://goreportcard.com/report/github.com/richinsley/jumpboot)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Jumpboot is a Go library that simplifies integrating Python into your Go applications.  It provides a robust and flexible way to:

*   **Create and manage isolated Python environments** using either [micromamba](https://mamba.readthedocs.io/en/latest/user_guide/micromamba.html) (a fast, lightweight conda implementation) or standard Python `venv`.
*   **Install Python packages** via both `pip` and `conda` (through micromamba).
*   **Run Python code** in several ways:
    *   Execute Python scripts.
    *   Run code snippets within a persistent REPL-like environment.
    *   Execute arbitrary Python code with JSON-based input/output.
*   **Share data efficiently** (optional) using shared memory and semaphores (requires CGO on Linux/macOS).
*  **Freeze and recreate** python environments for maximum reproducibility.

Jumpboot avoids the complexities of direct CGO bindings for general Python interaction, offering a cleaner and more maintainable approach using a bootstrap process. It's perfect for Go projects that need to leverage Python libraries or scripts without sacrificing performance or portability.

## Why Jumpboot?

*   **Simplified Integration:**  Easily embed Python functionality into your Go applications without complex setup or external dependencies (beyond `micromamba` itself, which Jumpboot can automatically download).
*   **Environment Isolation:**  Prevent conflicts between your Go project's dependencies and your Python code's dependencies. Each Python environment is self-contained.
*   **Flexibility:**  Choose between micromamba (for speed and conda compatibility) and `venv` (for standard Python virtual environments) based on your needs.
*   **Performance:**  Communicate with Python processes via efficient pipes.  Optionally use shared memory for zero-copy data transfer when performance is critical.
*   **Reproducibility:** Freeze environments to JSON files and recreate them later, ensuring consistent behavior across different systems and deployments.
*   **No CGO (Generally):**  Avoids the build-time and runtime complexities of CGO for most operations. CGO is only used for the *optional* shared memory and semaphore features (on non-Windows platforms).
*  **Windows, Linux and MacOS Support**: Create and manage python environments on all major operating systems.

## Table of Contents

*   [Environment Creation and Management](ENVIRONMENTS.md)
*   [Defining Python Programs (Modules and Packages)](PROGRAMS.md)
*   [Using the REPL Runtime](REPL.md)
*   [Examples](examples/README.md)
*   [Bootstrapping Process](BOOTSTRAP.md)

## Installation
```bash
go get https://github.com/richinsley/jumpboot
```

## Quickstart

This example shows the basics of creating a micromamba-based environment, installing a package, and running a simple Python script.  See the examples/ directory for more detailed use cases.
```go
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/richinsley/jumpboot"
)

func main() {
	// Create a temporary directory for the environment.
	tempDir, err := os.MkdirTemp("", "jumpboot-example")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tempDir) // Clean up after the example.

	// Create a new environment.
	env, err := jumpboot.CreateEnvironmentMamba(
		"my-example-env", // Environment name
		tempDir,        // Root directory
		"3.9",          // Python version
		"conda-forge",  // Conda channel
		nil,            // Optional progress callback (can be nil)
	)
	if err != nil {
		log.Fatal(err)
	}

    fmt.Printf("Environment created: %s\n", env.EnvPath)

	// Install a package using pip.
	err = env.PipInstallPackage("requests", "", "", true, nil) //install requests, no cache
	if err != nil {
		log.Fatal(err)
	}

    fmt.Println("Requests package installed")

	// Run a simple Python script.
	scriptPath := filepath.Join(tempDir, "my_script.py")
	err = os.WriteFile(scriptPath, []byte("import requests\nprint(requests.get('[https://www.google.com](https://www.google.com)').status_code)"), 0644)
	if err != nil {
		log.Fatal(err)
	}

	output, err := env.RunPythonReadCombinedOutput(scriptPath) //get combined stdout and stderr output
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Script output:\n%s\n", output)
}
```

## Advanced Usage

For more advanced use cases and to understand the internals of Jumpboot, see the following documentation:

*   [REPL Runtime](REPL.md):  Details on using the REPL-like Python process.
*   [Bootstrapping Process](BOOTSTRAP.md):  Explanation of how Jumpboot initializes Python processes.