# Jumpboot REPL Runtime

Jumpboot provides a REPL (Read-Eval-Print Loop) runtime for interacting with a persistent Python process. This allows you to execute Python code snippets incrementally, maintaining state between executions, similar to a Python interpreter session. This document explains how the REPL runtime works and how to use it effectively.

## Overview

The REPL runtime is built on top of Jumpboot's core process management capabilities.  It uses the same two-stage bootstrap process as described in `BOOTSTRAP.md`, but instead of executing a single script, it launches a Python process running a custom REPL loop (`scripts/repl.py`). This loop continuously reads code from the Go process, executes it, and sends back the results.

## Key Components

*   **`REPLPythonProcess` (Go):**  This Go struct manages the underlying `PythonProcess` and provides methods for interacting with the REPL:
    *   `NewREPLPythonProcess()`: Creates a new REPL process.  It takes optional key-value pairs (passed to the Python `jumpboot` module), environment variables, and lists of modules and packages.
    *   `Execute(code string, combinedOutput bool)`:  Executes a string of Python code within the REPL.  `combinedOutput` determines whether stdout and stderr are combined into a single output string.  This method is *blocking* and waits for the Python process to complete.
    *   `ExecuteWithTimeout(code string, combinedOutput bool, timeout time.Duration)`:  Similar to `Execute`, but with a timeout. If the Python code doesn't complete within the timeout, the Python process is terminated, and an error is returned.  The `REPLPythonProcess` becomes unusable after a timeout. This method is *non-blocking* (but waits up to the timeout).
    *   `Close()`:  Terminates the REPL process.
    *   `PythonProcess`: Provides access to the underlying `PythonProcess`, allowing for lower-level interaction if needed (e.g., direct access to stdin/stdout/stderr).
*   **`scripts/repl.py` (Python):** This embedded Python script implements the REPL loop.  It uses `code.InteractiveConsole` as a base class, providing standard REPL behavior (like handling incomplete input).  Key aspects:
    *   **Delimiter-Based Communication:**  The REPL script uses a custom delimiter (`\x01\x02\x03\n`, or `\x01\x02\x03\r\n` on Windows) to mark the end of code input and output.  This allows for multi-line code and output to be transmitted reliably over the pipes.
    *   **`conrun()`:** A modified `runsource()` method.  This is the core of the REPL loop. It takes the received code, executes it within the `InteractiveConsole`, and captures stdout and stderr (using `io.StringIO` and `contextlib.redirect_stdout`/`redirect_stderr`).
    *   **`__CAPTURE_COMBINED__` Variable:**  This variable (within the `scripts/repl.py` script) controls whether stdout and stderr are combined.  The Go code can modify this variable *within the running Python process* by sending a specially formatted command.
    *   **Error Handling:**  Exceptions during code execution are caught, and the traceback is sent back to the Go process.
    *  **Input Loop**: The REPL script continuously calls the `jumpboot.Pipe_in.readline()` method to retrieve commands from the go program.

## Usage and Behavior

1.  **Initialization:**  You create a REPL process using `env.NewREPLPythonProcess()`.  This starts the Python process with the `scripts/repl.py` script.

2.  **`Execute()`:**
    *   The Go code calls `Execute()` with a string of Python code.
    *   The code string is cleaned up (extra newlines removed, trailing whitespace trimmed).
    *   The delimiter is appended to the code.
    *   The code is written to the Python process's standard input (`PipeOut`).
    *   The Go code then *blocks*, reading from the Python process's standard output (`PipeIn`) until the delimiter is encountered.  The accumulated output is returned.

3.  **`ExecuteWithTimeout()`:**
    *   Similar to `Execute()`, but a timeout is specified.
    *   A goroutine is launched to read the output from the Python process.
    *   A `select` statement is used to wait for either the output, an error, or the timeout.
    *   If the timeout occurs, the Python process is terminated, and an error is returned. The `REPLPythonProcess` is marked as `closed` and is no longer usable.

4.  **State Persistence:**  The Python process maintains state between calls to `Execute()`.  Variables, function definitions, and imported modules persist until the process is closed.

5.  **Combined Output:**  The `combinedOutput` flag controls whether stdout and stderr are combined. By default, it's `true`.  You can change this dynamically by sending the special command `__CAPTURE_COMBINED__ = True` or `__CAPTURE_COMBINED__ = False` using `Execute()`.  Exceptions in Python are `not` processed as Go errors, but are delivered in the Combined Output.

6. **Closing:**  You must call `Close()` on the `REPLPythonProcess` to terminate the Python process gracefully.

## Sample
```go
package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"time"

	jumpboot "github.com/richinsley/jumpboot"
)

func main() {
	env, err := jumpboot.CreateEnvironmentFromSystem()
	if err != nil {
		fmt.Printf("Error creating environment: %v\n", err)
		return
	}
	repl, _ := env.NewREPLPythonProcess(nil, nil, nil, nil)
	defer repl.Close()

	// copy output from the Python script
	go func() {
		io.Copy(os.Stdout, repl.PythonProcess.Stdout)
		fmt.Println("Done copying stdout")
	}()

	go func() {
		io.Copy(os.Stderr, repl.PythonProcess.Stderr)
		fmt.Println("Done copying stderr")
	}()

	var result string

	result, err = repl.Execute("2 + 2", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: 4

	result, err = repl.Execute("print('Hello, World!')", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: Hello, World!

	result, err = repl.Execute("import math; math.pi", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: 3.141592653589793

	result, err = repl.Execute("ixvar = 2.0", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: ""

	result, err = repl.Execute("print(ixvar)", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: 2.0

	result, err = repl.Execute("print(1 / 0)", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: Traceback...

	pscript := `
for i in range(1, 11): 
	print(i)
`

	// turn off combined output and print howdy
	result, err = repl.Execute("print(ixvar)", false)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: ""

	// turn on combined output and print howdy
	result, err = repl.Execute("print('howdy')", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: howdy

	// turn on combined output and print howdy
	result, err = repl.Execute(pscript, true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: howdy

	factors := `
def factors(n):
    if n < 1:
        return "Factors are only defined for positive integers"
    
    factor_list = []
    for i in range(1, int(n**0.5) + 1):
        if n % i == 0:
            factor_list.append(i)
            if i != n // i:
                factor_list.append(n // i)
    
    return sorted(factor_list)
`
	// give the factor function to the python interpreter
	result, err = repl.Execute(factors, true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result)

	// we can now call the factors function from the python interpreter as many times as we want
	// calculate the factorial of of all the numbers from 1 to 1000
	for i := 1; i <= 1000; i++ {
		result, err = repl.Execute(fmt.Sprintf("factors(%d)", i), true)
		if err != nil {
			fmt.Printf("Error executing code: %v\n", err)
			return
		}
		fmt.Printf("factorial(%d) = %s\n", i, result)
	}

	// create a python function that loops forever and sleeps for 1 second each iteration
	// this will cause the python interpreter to hang until we kill the process
	forever := `
import time
def forever():
	while True:
		print("Sleeping for 1 second")
		time.sleep(1)
`
	// give the forever function to the python interpreter
	// repl.Execute("import time", false)
	result, err = repl.Execute(forever, true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result)

	// call the forever function with a timeout of 3 seconds
	result, err = repl.ExecuteWithTimeout("forever()", true, 3*time.Second)
	if err != nil {
		// this is expected because the python interpreter is hanging
		fmt.Printf("%v\n", err)
	}
	fmt.Println(result)

	// now say goodbye from python - it will return an error because the python interpreter is closed because of the timeout
	result, err = repl.Execute("print('Goodbye!')", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
}
```

## Advantages
* Interactive-like Development: Allows for a workflow similar to using a Python REPL, which can be useful for prototyping and experimentation.
* State Management: Variables and definitions persist between executions, enabling more complex interactions.
* Timeout Control: ExecuteWithTimeout() prevents the Go program from hanging indefinitely if the Python code enters an infinite loop or takes too long.

## Limitations
* Communication Overhead: While pipes are efficient, there's still some overhead associated with inter-process communication compared to direct function calls within the same process.
* Concurrency: The REPLPythonProcess uses a mutex (rpp.m) to protect against concurrent access to the Python process. Only one Execute() or ExecuteWithTimeout() call can be active at a time for a given REPLPythonProcess instance. If you need to execute Python code concurrently, you should create multiple REPLPythonProcess instances.
* Exception Handling:  Exceptions in Python REPL are currently not automatically handled in a way where they are returned as a Go error in repl.Execute.