# JumpBoot Environments

## Table of Contents
- [Creating Python Environments](#Creating-Python-Environments)
    - [System installed Python](#System-installed-Python)
    - [Micromamba](#Micromamba)
    - [Venv](#Venv)
    - [Progress function](#Progress-function)
- [Managing Pip Packages](#Managing-Pip-Packages)

## Creating Python Environments
JumpBoot provides functionality to create and manage Python environments in various ways. It supports creating environments from system-installed Python, using [Micromamba](https://mamba.readthedocs.io/en/latest/user_guide/micromamba.html) to generate environments with specific Python versions, and creating virtual environments (venvs) from existing JumpBoot environments.

### System installed Python
Use this method when you want to work with the Python version already installed on your system. This is useful for quick setups or when you need to use system-wide packages.  

When creating a JumpBoot environment from the system installed Python, JumpBoot will attempt to use the version of python that is available from the PATH environment variable.  (On Windows systems, it will look for the Python launcher py.exe first).  If there is a currently enabled Conda environment or a Venv environment it will use that.
```go
package main

import (
	"fmt"
	"log"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

func main() {
	// create a jumpboot environment from the system installed Python
	system_env, err := jumpboot.CreateEnvironmentFromSystem()
	if err != nil {
		log.Fatalf("Error creating environment: %v", err)
	}

	// show the version of Python
	fmt.Printf("Python version: %s\n", system_env.PythonVersion.String())
	// show the location of the Python executable
	fmt.Printf("Python executable: %s\n", system_env.PythonPath)
}
```
Alternatively, you can create a JumpBoot environment from an absolute path to a Python binary:
```go
pythonPath := "/usr/bin/python3"
CreateEnvironmentFromExacutable(pythonPath)
```

### Micromamba
Use Micromamba when you need a specific Python version or want to create isolated environments with precise control over dependencies. This is particularly useful for reproducible development environments.

When using Micromamba to create a python environment, JumpBoot will retrieve the most current version of Micromamba and use that to generate the environment.  If the environment already exists, JumpBoot skips the installation steps and makes the Environment object available.
```go
package main

import (
	"fmt"
	"os"
	"path/filepath"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

func main() {
	// Specify where the environments will be created
	// The root directory will contain all the environments
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "environments")
	fmt.Println("Creating Jumpboot repo at: ", rootDirectory)
	python_version := "3.12"
    env_name := "myenv"+python_version
	env, err := jumpboot.CreateEnvironmentMamba(env_name, rootDirectory, python_version, "conda-forge", nil)
	if err != nil {
		fmt.Printf("Error creating environment: %v\n", err)
		return
	}

	// if this is a new environment, we can install pip packages
	if env.IsNew {
        fmt.Printf("Created environment: %s\n", env.Name)
		fmt.Println("Installing numpy")
		err := env.PipInstallPackages([]string{"numpy"}, "", "", false, nil)
		if err != nil {
			fmt.Printf("Error installing numpy: %v\n", err)
		}
	} else {
        fmt.Println("Environment already exists")
    }

    // show the version of Python
	fmt.Printf("Python version: %s\n", system_env.PythonVersion.String())
	// show the location of the Python executable
	fmt.Printf("Python executable: %s\n", system_env.PythonPath)
}
```

### Venv
Creating Python environments with Venv allows for generating a base Python environment and using that to spawn new environments.  

Use Venv when you want to create lightweight, isolated Python environments based on an existing Python installation.
```go
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

func main() {
	// create a jumpboot environment from the system installed Python
	system_env, err := jumpboot.CreateEnvironmentFromSystem()
	if err != nil {
		log.Fatalf("Error creating environment: %v", err)
	}

	// Specify the root folder where the environments will be created
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "environments")

	// use the system env to generate a new virtual environment
	venvOptions := jumpboot.VenvOptions{
		SystemSitePackages: true,
		Upgrade:            true,
		Prompt:             "my-venv",
		UpgradeDeps:        true,
	}
	venv, err := jumpboot.CreateVenvEnvironment(system_env, filepath.Join(rootDirectory, "sysvenv"), venvOptions, nil)
	if err != nil {
		log.Fatalf("Error creating venv environment: %v", err)
	}

	// show the version of Python
	fmt.Printf("Python version: %s\n", venv.PythonVersion.String())
	// show the location of the Python executable
	fmt.Printf("Python executable: %s\n", venv.PythonPath)
}

```

### Progress function
JumpBoot provides a flexible progress tracking system for showing Python environment and Pip packages installation progress using callback functions:
```go
progressCallback := func(message string, current, total int64) {
    if total > 0 {
        fmt.Printf("%s: %d%%\n", message, int(float64(current)/float64(total)*100))
    } else {
        fmt.Printf("%s: In progress...\n", message)
    }
}

env, err := jumpboot.CreateEnvironmentMamba(env_name, rootDirectory, python_version, "conda-forge", progressCallback)
	if err != nil {
		fmt.Printf("Error creating environment: %v\n", err)
		return
	}
```

## Managing Pip Packages
JumpBoot provides several methods to install pip packages within your Python environments. These functions allow you to install single packages, multiple packages, or packages from a requirements file.

### Installing a Single Package
To install a single pip package, use the `PipInstallPackage` method:

```go
err := env.PipInstallPackage("numpy", "", "", false, progressCallback)
if err != nil {
    log.Fatalf("Error installing package: %v", err)
}
```
This function takes the following parameters:
* packageToInstall (string)
* index_url (string, optional)
* extra_index_url (string, optional)
* no_cache (bool)
* progressCallback (function)

### Installing Multiple Packages
To install multiple pip packages at once, use the PipInstallPackages method:
```go
packages := []string{"numpy", "pandas", "matplotlib"}
err := env.PipInstallPackages(packages, "", "", false, progressCallback)
if err != nil {
    log.Fatalf("Error installing packages: %v", err)
}
```
This function takes similar parameters to PipInstallPackage, but accepts a slice of package names instead of a single string.

### Installing from a Requirements File
To install packages from a requirements.txt file, use the PipInstallRequirements method:
```go
err := env.PipInstallRequirements("/path/to/requirements.txt", progressCallback)
if err != nil {
    log.Fatalf("Error installing requirements: %v", err)
}
```
This function takes the path to the requirements file and a progress callback function.

### Additional Options
All pip installation methods support the following options:
* index_url: You can specify a custom PyPI index URL to install packages from a different source.
* extra_index_url: You can provide an additional index URL as a fallback.
* no_cache: Set this to true to prevent pip from using its cache during installation.
* progressCallback: All methods accept a progress callback function to track the installation progress.

### Example with Custom Options
```go
packages := []string{"numpy", "pandas"}
indexUrl := "https://pypi.org/simple"
extraIndexUrl := "https://your-custom-index.com"
noCache := true

err := env.PipInstallPackages(packages, indexUrl, extraIndexUrl, noCache, progressCallback)
if err != nil {
    log.Fatalf("Error installing packages: %v", err)
}
```