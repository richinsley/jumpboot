# Jumpboot Bootstrapping Process

This document explains the internal bootstrapping process used by Jumpboot to initialize and run Python code within a Go application.  Understanding this process is helpful for advanced usage and debugging.

## Overview

Jumpboot uses a two-stage bootstrapping process to execute Python code without relying on direct CGO bindings (except for the optional shared memory features).  This approach enhances portability and reduces complexity. The core idea is to launch a Python subprocess and communicate with it via pipes.

The two stages are:

1.  **Primary Bootstrap (Go Side):** A small, embedded Python script (`scripts/bootstrap.py`) is executed as the initial command passed to the Python interpreter.  This script's primary responsibility is to set up the communication channels (pipes) and then execute the secondary bootstrap script.

2.  **Secondary Bootstrap (Python Side):**  A more extensive Python script (`scripts/secondaryBootstrapScript.py`) is read from a pipe (established in the primary bootstrap) and executed.  This script handles:
    *   Loading the embedded Python modules and packages (including `jumpboot` itself).
    *   Setting up a custom module finder (`CustomFinder`) and loader (`CustomLoader`) to handle imports from the embedded code.
    *   Importing the main Python program to be executed.
    *   (Optionally) starting a debugpy server for debugging.
    *   Finally, executing the main Python program.

## Detailed Steps

### 1. Primary Bootstrap (Go Side)

When you call a function like `NewPythonProcessFromString` or `NewPythonProcessFromProgram` in your Go code, the following happens:

*   **Pipes are Created:**  Go creates several pipes:
    *   `pipein_reader_primary`, `pipein_writer_primary`: For sending input to the Python process's standard input.
    *   `pipeout_reader_primary`, `pipeout_writer_primary`: For receiving output from the Python process's standard output.
    *   `reader_bootstrap`, `writer_bootstrap`:  For sending the secondary bootstrap script to the Python process.
    *   `reader_program`, `writer_program`: For sending program metadata (like module definitions) to the Python process (used by `NewPythonProcessFromProgram`).
*   **File Descriptors (FDs):** The file descriptors (FDs) of the read ends of the pipes (`reader_bootstrap`, `reader_program`, and primary pipe FDs) are obtained. On Windows, these are converted to handles.
*   **`primaryBootstrapScript` Generation:**  The embedded `scripts/bootstrap.py` script is treated as a Go template. The FD of `reader_bootstrap` is passed into the template and embedded directly into the Python script.  This allows the primary bootstrap script to know *which* file descriptor to read the secondary bootstrap script from.
*   **`exec.Command` Creation:**  A new `exec.Command` is created to launch the Python interpreter. The arguments passed to `exec.Command` are crucial:
    *   `-u`:  Unbuffered stdout/stderr.  This is important for real-time communication with the Python process.
    *   `-c`:  Execute the following string as Python code.  This string is the *primary* bootstrap script.
    *   `[FD of reader_bootstrap]`: The FD, passed as a string.
    *   `[count of extra FDs]`: number of extra files passed (for NewPythonProcessFromProgram)
    *   `[FDs of extra files]`: file descriptors for shared memory handles, etc.
    *   `[remaining args]`: command line arguments to pass to the python program
*   **Environment Variables:**  Any environment variables specified by the user are set for the Python process.
*   **`ExtraFiles` (Unix) / `AdditionalInheritedHandles` (Windows):**  The read ends of the pipes (`reader_bootstrap`, `reader_program`, and primary pipes) are passed to the child process (the Python interpreter).  This is how the Python process can access these pipes. On Unix-like systems, this is done via `cmd.ExtraFiles`. On Windows, this is done via `cmd.SysProcAttr.AdditionalInheritedHandles`.
*   **`cmd.Start()`:** The Python process is launched.
*   **Secondary Script and Data Transmission (Go Side - in goroutines):**
    *   **Secondary Bootstrap Script:** The `secondaryBootstrapScript.py` (also templated with the FD of `reader_program`) is written to `writer_bootstrap` and the pipe is closed.  Closing the writer signals EOF to the Python process, indicating the end of the script.
    *   **Program Data (if applicable):** If using `NewPythonProcessFromProgram`, the serialized `PythonProgram` data (containing module definitions, etc.) is written to `writer_program`, and the pipe is closed.
* **sys.__jbo**: In `scripts/bootstrap.py`, a helper function `o(h, m='r')` is added to `sys`.  This takes the handle of the pipe, and returns a file-like object.  This allows for platform independent file reads.

### 2. Secondary Bootstrap (Python Side)

Once the primary bootstrap script (`scripts/bootstrap.py`) starts running in the Python subprocess, the following occurs:

*   **`sys.__jbo` Definition:** defines a function for opening file handles to file descriptors
*   **Reading the Secondary Script:** The primary bootstrap script reads the entire contents of the secondary bootstrap script from the file descriptor passed as a command-line argument (obtained from the templated code). It then uses `exec()` to execute this secondary script.
*   **Pipe Setup (Secondary Script):** The secondary bootstrap script opens the necessary pipes for communication based on FDs provided in the `program_data`.
*   **`load_program_data` Function:** This function (within `secondaryBootstrapScript.py`) takes the JSON program data (passed via a pipe in `NewPythonProcessFromProgram`) and deserializes it. It creates a dictionary (`modules`) representing the structure of the embedded Python program, including packages, modules, and their source code (base64 encoded).
*   **Custom Module System:**
    *   **`CustomFinder`:** This class implements the `MetaPathFinder` interface. It intercepts import requests and checks if the requested module is present in the `modules` dictionary. If found, it creates a `ModuleSpec` using a `CustomLoader`.
    *   **`CustomLoader`:** This class implements the `Loader` interface.  It's responsible for:
        *   Creating module objects (if needed).
        *   Executing the module's code (using the provided source code).
        *   Setting standard module attributes (`__file__`, `__package__`, `__loader__`, etc.). Crucially, it uses the *virtual path* of the module (from the `program_data`) for `__file__`, allowing relative imports within the embedded code to work correctly.
        *   Adds the code to `linecache.cache` so that tracebacks work correctly and show the correct file and line number.
    *   **`sys.meta_path` Modification:** The `CustomFinder` instance is *prepended* to `sys.meta_path`. This ensures that Jumpboot's custom import mechanism is checked *before* Python's standard import system.
*   **Package Initialization:**  All top-level packages are loaded.
*   **`jumpboot` Package Setup:** The `jumpboot` package is loaded, and the input/output pipes (`f_in`, `f_out`) are attached as attributes (`jumpboot.Pipe_in`, `jumpboot.Pipe_out`). Any key-value pairs passed from the Go side are also added as attributes to the `jumpboot` module.
*   **Main Module Execution:** Finally, the code of the main module (specified by `program_data['Program']['Name']`) is executed.  This is the user's Python code that will interact with the Go program.
*   **Optional Debugpy Setup:** If a debug port is specified, the `debugpy` library is used to start a debugging server and wait for a client to connect (allowing remote debugging of the Python code).

## Key Concepts

*   **Pipes:**  Pipes are the primary communication mechanism between the Go process and the Python subprocess.  They provide a reliable, unidirectional byte stream.
*   **File Descriptors (FDs) / Handles:**  FDs (Unix) and Handles (Windows) are numerical identifiers for open files (and pipes).  Passing these to the child process allows it to access the same pipes as the parent process.
*   **`exec.Command`:**  The Go standard library's `exec.Command` is used to create and manage the Python subprocess.  The `-u` and `-c` flags are essential for correct operation.
*   **`sys.meta_path`:** This list in Python controls the import process. By adding a custom finder to the beginning of this list, Jumpboot overrides Python's default import behavior for modules managed by Jumpboot.
*   **`Loader` Interface:**  The `CustomLoader` implements the `Loader` interface, providing the logic for loading and executing the embedded Python code.
*   **`linecache`:**  The `linecache` module allows to access the source code of the modules to be used in tracebacks, etc.

## Advantages of this Approach

*   **No CGO (Mostly):**  Avoids the complexities and platform-specific issues of using CGO for general interaction with Python.
*   **Isolation:** The Python code runs in a separate process, providing strong isolation and preventing conflicts with the Go runtime.
*   **Flexibility:** Supports both micromamba and `venv` environments.
*   **Portability:** Works consistently across Windows, macOS, and Linux.

## Limitations

*   **Overhead:**  Launching a separate process has some overhead compared to direct CGO calls. However, for many use cases, this overhead is negligible compared to the benefits of isolation and simplicity.
*   **Communication:** Communication is primarily through pipes, which are efficient for byte streams but require serialization (e.g., JSON) for structured data. The *optional* shared memory feature mitigates this for specific high-performance scenarios.