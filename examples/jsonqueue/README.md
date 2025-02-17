# JumpBoot: Go and Python Inter-Process Communication Example (Command/Response)

This example demonstrates how to use the JumpBoot library to establish bidirectional communication between a Go program and a Python script using a JSON-based message queue over standard input/output pipes.

**Key Concepts:**

*   **JumpBoot:** A Go library that simplifies the creation and management of Python environments (using micromamba or venv) and provides tools for running Python code from Go, including inter-process communication.
*   **JSONQueue:** A Python class (provided by JumpBoot's `jumpboot` package) that handles sending and receiving JSON messages over pipes.
*   **Standard Input/Output (Pipes):** The primary communication channel between the Go program and the Python script. Go writes JSON commands to Python's standard input, and Python writes JSON responses to its standard output.
*   **Command/Response Pattern:**  The Go program sends commands to the Python script, and the Python script processes the commands and (optionally) sends back responses.
*   **Concurrency:** Go routines (`go func() { ... }`) are used to handle reading from the Python process's standard output and standard error concurrently, preventing deadlocks.

**Project Structure:**

*   **`main.go`:** The main Go program that:
    *   Creates a Python environment using JumpBoot.
    *   Starts a Python process running the `modules/command_response.py` script.
    *   Sends commands to the Python process using the `sendCommand` function.
    *   Receives and processes responses from the Python process.
    *   Handles the "exit" command gracefully.
*   **`modules/command_response.py`:** The Python script that:
    *   Uses `jumpboot.JSONQueue` to receive commands and send responses.
    *   Defines a `handle_command` function to process different commands.
    *   Enters a loop to continuously receive and process commands until an "exit" command is received or an error occurs.
* `../environments`: This is where JumpBoot will create the python environment

**Code Breakdown:**

**`main.go`**

1.  **Environment Setup:**
    ```go
    cwd, _ := os.Getwd()
    rootDirectory := filepath.Join(cwd, "..", "environments")
    version := "3.12"
    env, err := jumpboot.CreateEnvironmentMamba("example1_env", rootDirectory, version, "conda-forge", nil)
    // ... error handling ...
    ```
    This code creates a new Python environment named "example1_env" using micromamba.  If an environment with this name already exists, it will be reused. The `rootDirectory` is set to the `../environments` directory, relative to the current working directory. The version is set to "3.12"

2.  **Python Program Definition:**
    ```go
    program := &jumpboot.PythonProgram{
        Name: "CommandResponse",
        Path: cwd,
        Program: jumpboot.Module{
            Name:   "__main__",
            Path:   filepath.Join(cwd, "modules", "command_response.py"),
            Source: base64.StdEncoding.EncodeToString([]byte(commandResponseScript)),
        },
    }
    ```
    This defines the Python program to be executed.  The main script is embedded in the Go binary using `//go:embed` and then base64-encoded.

3.  **Starting the Python Process:**
    ```go
    pyProcess, _, err := env.NewPythonProcessFromProgram(program, nil, nil, false)
    // ... error handling ...
    defer pyProcess.Terminate()
    ```
    This starts the Python process using the defined program and environment. `pyProcess.Terminate()` is deferred to ensure the Python process is killed if the Go program exits unexpectedly.

4.  **I/O Goroutines:**
    ```go
    go func() {
        io.Copy(os.Stdout, pyProcess.Stdout)
    }()
    go func() {
        io.Copy(os.Stderr, pyProcess.Stderr)
    }()
    ```
    These goroutines continuously copy the Python process's standard output and standard error to the Go program's standard output and standard error, respectively. This allows you to see output from the Python script in real-time.

5.  **`sendCommand` Function:**
    ```go
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

    ```
    This function encapsulates the logic for sending a command to the Python process and optionally waiting for a response.  It takes the command name, data to send, and a boolean flag `waitForResponse`.
    *   It marshals the command and data into a JSON object.
    *   It writes the JSON data to the Python process's standard input (through `pyProcess.PipeOut`).  A newline character (`\n`) is appended to the JSON string because the Python script uses `readline()`.
    *   If `waitForResponse` is `true`, it reads a line from the Python process's standard input (through `pyProcess.PipeIn`), unmarshals the JSON data into a map, and returns the map.
    *   If `waitForResponse` is `false`, it returns `nil, nil` immediately.
    *   It includes error handling for writing to and reading from the pipes, as well as for JSON marshaling and unmarshaling.

6.  **Example Commands:**
    ```go
        // ... (inside main function) ...
        response, err := sendCommand("greet", map[string]string{"name": "Go Program"}, true)
        // ... process response ...

        result, err := sendCommand("calculate", map[string]int{"x": 5, "y": 7}, true)
        // ... process result ...

        _, err = sendCommand("exit", nil, false) // Don't wait for a response
        // ... error handling ...
    ```
    These lines demonstrate how to use the `sendCommand` function to send different commands ("greet", "calculate", "exit") to the Python script.  Notice that the "exit" command is sent with `waitForResponse` set to `false`.

7.  **Waiting for Python to Exit:**
    ```go
    pyProcess.Wait() // Important to wait for the process to finish
    ```
    This line ensures that the Go program waits for the Python process to exit before terminating itself.  This is important for proper resource cleanup.

**`modules/command_response.py`**

1.  **JSONQueue Initialization:**
    ```python
    import jumpboot
    queue = jumpboot.JSONQueue(jumpboot.Pipe_in, jumpboot.Pipe_out)
    ```
    This initializes a `JSONQueue` object, which is used for sending and receiving JSON messages. `jumpboot.Pipe_in` and `jumpboot.Pipe_out` are file-like objects provided by JumpBoot, representing the standard input and standard output pipes, respectively.

2.  **`handle_command` Function:**
    ```python
    def handle_command(command, data):
        if command == "greet":
            name = data.get("name", "World")
            return {"message": f"Hello, {name}!"}
        elif command == "calculate":
            x = data.get("x", 0)
            y = data.get("y", 0)
            return {"result": x + y}
        elif command == "long_running":
            cancel_flag.clear() # Reset the flag
            for i in range(10):
                if cancel_flag.is_set():
                    print("Long running task cancelled.")
                    return {"status": "cancelled"}
                print(f"Long running task: step {i}")
                time.sleep(1)
            return {"status": "completed"}
        elif command == "cancel":
            cancel_flag.set() # Set the cancellation flag
            return None  # No response needed
        else:
            return {"error": "Unknown command"}
    ```
    This function takes a command name and data as input, processes the command, and returns a response (which can be `None` if no response is needed). It includes examples of accessing data sent from Go using `.get()` with default values, which is good practice.

3.  **Main Loop:**
    ```python
    def main():
        print("Python process started, waiting for commands...")
        try:
            while True:
                try:
                    message = queue.get(block=True, timeout=5)
                except TimeoutError:
                    # print("Timeout waiting for message. Checking if parent is still alive...") # Too verbose
                    continue
                except EOFError:
                    print("Pipe closed, exiting.")
                    break
                except Exception as e:
                    print(f"Error reading message: {e}", file=sys.stderr)
                    break

                if message is None:
                    continue

                command = message.get("command")
                data = message.get("data")

                if command == "exit":
                    print("Received exit command, exiting.")
                    break

                response = handle_command(command, data)
                if response is not None:
                    queue.put(response)

        except Exception as e:
            print("Error in main loop:", e)
        finally:
            print("python exiting...")
    ```
    *   The main loop continuously reads messages from the `queue` using `queue.get(block=True, timeout=5)`.  The `block=True` makes it wait for a message, and `timeout=5` prevents it from blocking indefinitely if the Go program crashes or closes the pipe unexpectedly.
    *   It handles `TimeoutError` (if no message is received within the timeout) and `EOFError` (if the pipe is closed).
    *   It extracts the `command` and `data` from the received JSON message.
    *   If the command is "exit", it breaks out of the loop, causing the Python script to terminate.
    *   Otherwise, it calls `handle_command` to process the command and sends the response back to Go using `queue.put(response)`.
    * The `finally` block ensures that a "python exiting..." message is printed even if an error occurs.

**How to Run the Example:**

1.  **Save:** Save the Go code as `main.go` and the Python code as `modules/command_response.py` in the appropriate directory structure.
2.  **Build:**  From the directory containing `main.go`, run `go build`. This will create an executable named `main` (or `main.exe` on Windows).
3.  **Run:** Execute the compiled binary: `./main` (or `main.exe` on Windows).

You should see output similar to this:
```
Creating Jumpboot repo at:  ../environments
Created environment: example1_env
Python process started, waiting for commands...
Greeting response: map[message:Hello, Go Program!]
Calculation result: map[result:12]
Long running task: step 0
Long running task: step 1
Long running task: step 2
Long running task: step 3
Long running task: step 4
Long running command returned error: failed to read from Python: EOF
Long running task cancelled.
Received exit command, exiting.
python exiting...
Python process exited.
```