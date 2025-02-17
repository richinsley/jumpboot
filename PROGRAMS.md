# Constructing and Using `jumpboot.PythonProgram`

This document explains how to structure and use the `jumpboot.PythonProgram` type in Go to define and run Python code within a Jumpboot-managed environment.  This includes how to define modules and packages, and how to include embedded Python code.

## Overview

The `jumpboot.PythonProgram` struct allows you to define a Python program's structure *within your Go code*. This includes specifying:

*   **The main module:**  The entry point for your Python code.
*   **Supporting modules:**  Individual Python files.
*   **Packages:**  Collections of modules organized into directories (with `__init__.py` files).
*   **Key-Value Pairs:**  Data passed directly to the Python `jumpboot` module.
*   **Debug Options:** Settings for using the `debugpy` debugger.

This structure is then serialized to JSON and passed to the Python process during the bootstrapping process (see `BOOTSTRAP.md`). The custom module finder and loader in the secondary bootstrap script (`scripts/secondaryBootstrapScript.py`) use this information to make the modules and packages available for import within the Python environment.

## `PythonProgram` Structure

The `PythonProgram` struct is defined as follows:

```go
type PythonProgram struct {
    Name     string
    Path     string
    Program  Module
    Packages []Package
    Modules  []Module
    PipeIn   int
    PipeOut  int
    // DebugPort - setting this to a non-zero value will start the debugpy server on the specified port
    // and wait for the debugger to attach before running the program in the bootstrap script
    DebugPort    int
    BreakOnStart bool
    KVPairs      map[string]interface{}
}
```

* `Name`: A string identifier for your Python program.
* `Path`: A string representing the virtual path to your main program. This doesn't have to be a real filesystem path, as the code will be loaded from the embedded data.
* `Program`: A Module struct representing the main entry point of your Python program (equivalent to __main__.py).
* `Packages`: A slice of Package structs, representing any Python packages your program uses.
* `Modules`: A slice of Module structs, representing any individual Python modules (files) that are not part of a package.
* `PipeIn / PipeOut`: File descriptors for input/output (automatically handled by Jumpboot).
* `DebugPort`: If set to a non-zero value, the Python process will start a debugpy server on this port and wait for a debugger to attach.
* `BreakOnStart`: If true, and DebugPort is set, a breakpoint will happen on the first line.
* `KVPairs`: A map of key-value pairs that will be made available as attributes of the jumpboot module in the Python environment. This allows you to pass configuration data or other information from Go to Python.

## `Module` Structure
```go
type Module struct {
    // Name of the module
    Name string
    // Path to the module
    Path string
    // Base64 encoded source code of the module
    Source string
}
```
* `Name`: The name of the module (e.g., "mymodule.py" or "utils").
* `Path`: The virtual path to the module. This is important for relative imports within your Python code. For top-level modules, this might be something like "/virtual_modules/mymodule.py". For modules within packages, it should reflect the package structure (e.g., "/virtual_modules/mypackage/mymodule.py").
* `Source`: The base64 encoded source code of the module.

## `Package` Structure
```go
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
```
* `Name`: The name of the package (e.g., "mypackage").
* `Path`: The virtual path to the package's directory. This should not include the __init__.py filename (e.g., "/virtual_modules/mypackage").
* `Modules`: A slice of Module structs representing the modules within this package. This should include an __init__.py module.
* `Packages`: A slice of Package structs, representing any subpackages within this package.

## Creating Modules and Packages
Jumpboot provides helper functions for creating `Module` and `Package` objects:

* `NewModuleFromPath(name, path string)`: Creates a Module from a file on disk. Reads the file, base64 encodes the content, and sets the Name and Path.
* `NewModuleFromString(name, original_path string, source string)`: Creates a Module directly from a string containing the source code. This is useful for embedding Python code directly within your Go code. The original_path argument should be a path that reflects the module's location within the virtual file system you are creating.
* `NewPackage(name, path string, modules []Module)`: Creates a Package from a list of Module objects.
* `NewPackageFromFS(name string, sourcepath string, rootpath string, fs embed.FS)`: This is the most powerful way to create packages. It recursively constructs a Package from an embed.FS (an embedded filesystem). This allows you to embed entire package hierarchies directly within your Go binary.

## Example: Building a `PythonProgram`
Let's break down the [mlx](examples\mlx\main.go) example, illustrating how to build a `PythonProgram`:

```go
package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	jumpboot "github.com/richinsley/jumpboot"
)

//go:embed modules/models.py
var models string

//go:embed modules/utils.py
var utils string

//go:embed modules/generate.py
var generate string

func main() {
    // ... (Environment setup code) ...

	// the original mlx example exists as a program, not a package, so we'll load each module individually
	cwd, _ := os.Getwd()
	binpath := filepath.Join(cwd, "modules")
	utils_module := jumpboot.NewModuleFromString("utils", filepath.Join(binpath, "utils.py"), utils)
	models_module := jumpboot.NewModuleFromString("models", filepath.Join(binpath, "models.py"), models)
	generate_module := jumpboot.NewModuleFromString("generate", filepath.Join(binpath, "generate.py"), generate)

	// collect the modules into a slice
	modules := []jumpboot.Module{*utils_module, *models_module, *generate_module}

	// create a new REPL Python process with the modules
	repl, _ := env.NewREPLPythonProcess(nil, nil, modules, nil)
	defer repl.Close()

	// import the modules into the Python process that we'll need
	imports := `
import generate
import models
import mlx.core as mx
import jumpboot	`
	repl.Execute(imports, true)

    // ... (Rest of the REPL interaction) ...
}
```

1. **Embedding the Python Code:**
```go
//go:embed modules/models.py
var models string

//go:embed modules/utils.py
var utils string

//go:embed modules/generate.py
var generate string
```
The `//go:embed` directives embed the contents of the Python files (`models.py`, `utils.py`, `generate.py`) into Go string variables (`models`, `utils`, `generate`).

2. **Creating `Module` Objects:
```go
utils_module := jumpboot.NewModuleFromString("utils", filepath.Join(binpath, "utils.py"), utils)
models_module := jumpboot.NewModuleFromString("models", filepath.Join(binpath, "models.py"), models)
generate_module := jumpboot.NewModuleFromString("generate", filepath.Join(binpath, "generate.py"), generate)
```

`NewModuleFromString` is used to create `Module` objects from the embedded strings.  The first argument is the module name (without the `.py` extension).  The second is a path used for organizing and inports, simulating where the file would be, and the third is the source code (as a string).

3. **Creating a `PythonProgram` (Implicitly)**:
```go
modules := []jumpboot.Module{*utils_module, *models_module, *generate_module}

// create a new REPL Python process with the modules
repl, _ := env.NewREPLPythonProcess(nil, nil, modules, nil)
```
The `NewREPLPythonProcess` function implicitly creates a `PythonProgram` internally.  It's equivalent to doing this:
```go
program := &jumpboot.PythonProgram{
    Name: "JumpBootREPL", // Default name used by NewREPLPythonProcess
    Path: cwd, // Current working directory
    Program: jumpboot.Module{
        Name:   "__main__",
        Path:   filepath.Join(cwd, "modules", "repl.py"), // Path to the embedded REPL script
        Source: base64.StdEncoding.EncodeToString([]byte(replScript)),
    },
    Modules: modules, // Your custom modules
    Packages: []jumpboot.Package{}, // No packages in this example
}
process, _, err := env.NewPythonProcessFromProgram(program, nil, nil, false)
repl := &jumpboot.REPLPythonProcess{PythonProcess: process}
```
4. **Importing within Python:**
```go
imports := `
import generate
import models
import mlx.core as mx
import jumpboot	`
repl.Execute(imports, true)
```
This imports the created modules in the Python REPL. Because of the `CustomFinder` and `CustomLoader` in `scripts/secondaryBootstrapScript.py`, these imports will be resolved using the embedded code.

## Example: Using `NewPackageFromFS`
Let's say you have a directory structure like this:
```bash
mypackage/
├── __init__.py
├── module1.py
└── module2.py
```
And you want to embed this entire package.

Go Code:
```go
package main

import (
    "embed"
    "fmt"
    "log"
    "os"
    "path/filepath"

    jumpboot "github.com/richinsley/jumpboot"
)

//go:embed mypackage
var mypackageFS embed.FS

func main() {
    tempDir, err := os.MkdirTemp("", "jumpboot-example")
    if err != nil {
        log.Fatal(err)
    }
    defer os.RemoveAll(tempDir)

    env, err := jumpboot.CreateEnvironmentMamba("myenv", tempDir, "3.9", "conda-forge", nil)
    if err != nil {
        log.Fatal(err)
    }

    // Create the package from the embedded filesystem.
    // "mypackage" is the package name.
    // "" is the source path (within the embed.FS).
    // "mypackage" is the root path (within the embed.FS).
    pkg, err := jumpboot.NewPackageFromFS("mypackage", "", "mypackage", mypackageFS)
    if err != nil {
        log.Fatal(err)
    }

    // Create a simple main program.
    mainModule := jumpboot.NewModuleFromString("__main__", "/virtual_modules/__main__.py", `
import mypackage.module1
import mypackage.module2

mypackage.module1.my_function()
print(mypackage.module2.MY_CONSTANT)
`)

    program := &jumpboot.PythonProgram{
        Name:    "MyProgram",
        Path:    "/virtual_modules",
        Program: *mainModule,
        Packages: []jumpboot.Package{*pkg}, // Include the package
    }

    // You can now use NewPythonProcessFromProgram:
    process, _, err := env.NewPythonProcessFromProgram(program, nil, nil, false)
    if err != nil {
        log.Fatal(err)
    }
	defer process.Terminate()

    // Read and print output (optional)
    output, err := io.ReadAll(process.Stdout)
    if err != nil && err != io.EOF { //expect an EOF when done
        log.Fatal(err)
    }
	fmt.Println(string(output))
}
```
`mypackage/__init__.py`:
```python
# This can be empty, or contain package-level initialization code.
print("Initializing mypackage")
```
`mypackage/module1.py`:
```python
def my_function():
    print("Hello from module1!")
```
`mypackage/module2.py`:
```python
MY_CONSTANT = 42
```

Key points in this example:
* `//go:embed` mypackage: Embeds the entire `mypackage` directory.
* `NewPackageFromFS`: This function handles the recursive creation of the `Package` object, including handling __init__.py files correctly. It automatically creates `Module` objects for each `.py` file.
* `Virtual Paths`: The paths used within the `Module` and `Package` objects are virtual paths. They don't need to correspond to actual paths on the filesystem after the Go program is compiled. They are used for resolving imports *within* the embedded Python code. They should, however, mirror the structure of the original source directory.
* `Import statements`: The import statements in the main module uses the package structure as if `mypackage` was installed normally.