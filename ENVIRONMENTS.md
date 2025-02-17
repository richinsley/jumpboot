# Jumpboot: Environment Creation and Management

This document provides examples of how to create and manage Python environments using Jumpboot. It covers creating environments with micromamba, `venv`, and using the system's Python installation. It also demonstrates how to freeze an environment's configuration to a JSON file and restore it later.

## Creating Environments

Jumpboot supports three primary methods for creating Python environments:

1.  **Using Micromamba (Recommended):**  Micromamba is a fast and lightweight implementation of the conda package manager.  This is the recommended approach for most use cases.

2.  **Using `venv`:**  Creates a standard Python virtual environment using the built-in `venv` module.

3.  **Using System Python:**  Uses the system's default Python installation directly.  This is useful for simple scripts or when you don't need strict isolation.

### 1. Creating an Environment with Micromamba

```go
package main

import (
    "fmt"
    "log"
    "os"
    "path/filepath"

    jumpboot "github.com/richinsley/jumpboot"
)

func main() {
    // Create a temporary directory for the environment.  Good practice!
    tempDir, err := os.MkdirTemp("", "jumpboot-example")
    if err != nil {
        log.Fatal(err)
    }
    defer os.RemoveAll(tempDir) // Clean up when done.

    // Create a new environment with Python 3.9 and the conda-forge channel.
    env, err := jumpboot.CreateEnvironmentMamba(
        "my_env",       // Environment name
        tempDir,        // Root directory for environments
        "3.9",          // Python version
        "conda-forge",  // Conda channel
        nil,            // Progress callback (can be nil)
    )
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Created environment: %s\n", env.Name)
    fmt.Printf("  Python Path: %s\n", env.PythonPath)
    fmt.Printf("  Environment Path: %s\n", env.EnvPath)

    // Install some packages using Micromamba.
    err = env.MicromambaInstallPackage("numpy", "conda-forge")
    if err != nil {
        log.Fatal(err)
    }

    err = env.MicromambaInstallPackage("requests", "conda-forge")
    if err != nil {
        log.Fatal(err)
    }

    // Verify installation (optional)
    numpyVersion, err := jumpboot.RunReadCombinedOutput(env.PythonPath, "-c", "import numpy; print(numpy.__version__)")
    if err != nil {
        log.Fatalf("Error checking numpy version: %v, output: %s", err, numpyVersion)
    }
    fmt.Printf("Installed numpy version: %s\n", numpyVersion)
}
```
#### Explaination:
* `CreateEnvironmentMamba(envName, rootDir, pythonVersion, channel, progressCallback)`:
   * `envName`: The name of the new environment (e.g., "my_env").
   * `rootDir`: The base directory where environments will be created. Jumpboot will create a subdirectory `envs/<envName>` within this directory.
   * `pythonVersion`: The desired Python version (e.g., "3.9", "3.10").
   * `channel`: The conda channel to use (e.g., "conda-forge"). If empty, the default channel is used.
   * `progressCallback`: An optional function to receive progress updates. See the API documentation for details.
* `MicromambaInstallPackage(packageToInstall, channel)`: Installs a package using micromamba.

### 2. Creating a `venv` Environment
```go
package main

import (
    "fmt"
    "log"
    "os"
    "path/filepath"

    jumpboot "github.com/richinsley/jumpboot"
)

func main() {
    // First, create a base environment (e.g., using system Python).
    baseEnv, err := jumpboot.CreateEnvironmentFromSystem()
    if err != nil {
        log.Fatal(err)
    }

    // Create a temporary directory for the environment.
    tempDir, err := os.MkdirTemp("", "jumpboot-example")
    if err != nil {
        log.Fatal(err)
    }
    defer os.RemoveAll(tempDir)

    // Create a venv based on the system environment.
    venvPath := filepath.Join(tempDir, "my_venv")
    options := jumpboot.VenvOptions{} // Use default options.
    venvEnv, err := jumpboot.CreateVenvEnvironment(baseEnv, venvPath, options, nil)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Created venv: %s\n", venvEnv.Name)
    fmt.Printf("  Python Path: %s\n", venvEnv.PythonPath)

    // Install packages using pip
    err = venvEnv.PipInstallPackages([]string{"requests", "beautifulsoup4"}, "", "", true, nil) // true for no-cache
    if err != nil {
        log.Fatal(err)
    }

    // Verify installation (optional)
    requestsVersion, err := jumpboot.RunReadCombinedOutput(venvEnv.PythonPath, "-c", "import requests; print(requests.__version__)")
    if err != nil {
         log.Fatalf("Error checking requests version: %v, output: %s", err, requestsVersion)
    }
     fmt.Printf("Installed requests version: %s\n", requestsVersion)
}
```
#### Explaination:
* `CreateEnvironmentFromSystem()`: Gets the system's default Python installation.
   * `CreateVenvEnvironment(baseEnv, venvPath, options, progressCallback)`:
   * `baseEnv`: The base `Environment` to use (usually the system Python).
   * `venvPath`: The full path to the directory where the venv will be created.
   * `options`: A `VenvOptions` struct to control `venv` creation (e.g., using symlinks, including system site packages).
   * `progressCallback`: An optional progress callback.
* `PipInstallPackages`: installs a list of pip packages

### 3. Using the System Python Environment
```go
package main

import (
    "fmt"
    "log"

    jumpboot "github.com/richinsley/jumpboot"
)

func main() {
    env, err := jumpboot.CreateEnvironmentFromSystem()
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Using system Python: %s\n", env.PythonVersion)
    fmt.Printf("  Python Path: %s\n", env.PythonPath)
    fmt.Printf("  Pip Path: %s\n", env.PipPath)

    // You can use env.PipPath to install packages via pip (if available).
    //  Example (requires pip to be installed in the system Python):
    // output, err := jumpboot.RunReadCombinedOutput(env.PipPath, "install", "requests")
    // if err != nil {
    //     log.Fatalf("Error installing requests: %v, output: %s", err, output)
    // }
}
```
#### Explaination:
* `CreateEnvironmentFromSystem()`: Detects and uses the system's default Python installation.

## Freezing and Restoring Environments
Jumpboot allows you to "freeze" the configuration of an environment (installed packages, channels, Python version) to a JSON file and then recreate that environment later, ensuring reproducibility.
```go
package main

import (
    "fmt"
    "log"
    "os"
    "path/filepath"

    jumpboot "github.com/richinsley/jumpboot"
)

func main() {
    // 1. Create an environment and install packages.
    tempDir, err := os.MkdirTemp("", "jumpboot-example")
    if err != nil {
        log.Fatal(err)
    }
    defer os.RemoveAll(tempDir)

    env, err := jumpboot.CreateEnvironmentMamba("freeze_test", tempDir, "3.10", "conda-forge", nil)
    if err != nil {
        log.Fatal(err)
    }
    err = env.MicromambaInstallPackage("numpy", "conda-forge")
    if err != nil {
        log.Fatal(err)
    }
    err = env.PipInstallPackages([]string{"requests"}, "", "", true, nil)
    if err != nil {
        log.Fatal(err)
    }

    // 2. Freeze the environment to a JSON file.
    freezeFilePath := filepath.Join(tempDir, "environment.json")
    err = env.FreezeToFile(freezeFilePath)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Environment frozen to: %s\n", freezeFilePath)

    // 3. Create a new environment from the frozen file.
    restoredEnv, err := jumpboot.CreateEnvironmentFromJSONFile(freezeFilePath, tempDir, nil)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Environment restored: %s\n", restoredEnv.Name)
    fmt.Printf("  Python Path: %s\n", restoredEnv.PythonPath)

    // Verify the restored environment (optional)
    numpyVersion, err := jumpboot.RunReadCombinedOutput(restoredEnv.PythonPath, "-c", "import numpy; print(numpy.__version__)")
    if err != nil {
         log.Fatalf("Error checking numpy version in restored env: %v, output: %s", err, numpyVersion)
    }
    fmt.Printf("Restored numpy version: %s\n", numpyVersion)

     requestsVersion, err := jumpboot.RunReadCombinedOutput(restoredEnv.PythonPath, "-c", "import requests; print(requests.__version__)")
     if err != nil {
          log.Fatalf("Error checking requests version in restored env: %v, output: %s", err, requestsVersion)
     }
     fmt.Printf("Restored requests version: %s\n", requestsVersion)

}
```

#### Explaination:
* `FreezeToFile(filePath)`: Saves the environment's configuration to the specified JSON file.
* `CreateEnvironmentFromJSONFile(filePath, rootDir, progressCallback)`: Creates a new environment based on the JSON configuration. It uses the specified `rootDir` for the new environment.

## Running Python Scripts
With an environment, you can directly execute scripts from strings or files:
```go
package main

import (
    "fmt"
    "log"
    "os"
    "path/filepath"

    jumpboot "github.com/richinsley/jumpboot"
)

func main() {
	tempDir, err := os.MkdirTemp("", "jumpboot_envs")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

    env, err := jumpboot.CreateEnvironmentMamba("script_env", tempDir, "3.8", "conda-forge", nil)
    if err != nil {
        log.Fatal(err)
    }

    // Create a simple Python script.
    scriptPath := filepath.Join(tempDir, "test_script.py")
    scriptContent := "print('Hello from Python!')"
    err = os.WriteFile(scriptPath, []byte(scriptContent), 0644)
    if err != nil {
        log.Fatal(err)
    }

    // Run the script and capture the combined output.
    output, err := env.RunPythonReadCombinedOutput(scriptPath)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Script output:\n%s\n", output)

    // Example with arguments
    scriptWithArgs := `
import sys
print(f"Arg 1: {sys.argv[1]}")
print(f"Arg 2: {sys.argv[2]}")
`
    scriptPath2 := filepath.Join(tempDir, "test_script2.py")
    err = os.WriteFile(scriptPath2, []byte(scriptWithArgs), 0644)
    if err != nil {
        log.Fatal(err)
    }

    output2, err := env.RunPythonReadCombinedOutput(scriptPath2, "hello", "world")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Script output (with args):\n%s\n", output2)
}
```
#### Explaination:
* `RunPythonReadCombinedOutput`: A helper function that executes the python script located at scriptPath and captures the output. Additional arguments after the script path are passed to the python process
