# JumpBoot Gradio Integration Demo

This example demonstrates how to seamlessly integrate a Gradio user interface (running in Python) with Go functions using the JumpBoot library. It showcases bidirectional communication between Go and Python, allowing Gradio UI elements to trigger Go code execution and display the results.

## Functionality:

- Gradio UI: The Python script (`modules/gradio_app.py`) defines a simple Gradio interface with four tabs:

  - Add: Takes two numbers as input, calls a Go function to add them, and displays the result.
  - Greet: Takes a name as input, calls a Go function to generate a greeting, and displays the greeting.
  - Uppercase: Takes text as input, calls a Go function to convert it to uppercase, and displays the result.
  - Reverse: Takes a list of numbers, calls a Go function to reverse the list, and displays the result.
  - Quit: Sends an exit signal to the Go program.

- Go Backend: The Go program (main.go) performs the following:

  - Environment Setup: Creates or reuses a Python environment (using micromamba) and installs the gradio package if necessary.
  - Process Management: Starts the Python script as a subprocess, establishing communication via standard input/output pipes.
  - Function Mapping: Defines a map of Go functions that can be called from Python.
  - Command Handling: Listens for JSON messages from the Python process, extracts the command name and data, dynamically calls the corresponding Go function, and sends the result (or error) back to Python as a JSON response.
  - Concurrency: Uses goroutines to handle communication with the Python process concurrently, preventing blocking.
  - Graceful Shutdown: Includes a quit button and handles the exit signal.

- Communication:

  - Uses JumpBoot's JSONQueue (on the Python side) and standard Go JSON encoding/decoding to exchange messages over pipes.
  - Messages are JSON objects with the following structure:
    - Requests (Python to Go): {"command": "command_name", "data": ..., "request_id": "..."}
    - Responses (Go to Python): {"result": ..., "request_id": "..."} or {"error": "...", "request_id": "..."}
    - The "request_id" field is crucial for matching responses to the correct requests.

## How it Works:

1. The Go program starts the Python script as a subprocess.
2. The Python script initializes the Gradio interface. demo.queue() is called to enable Gradio's internal queuing, which is essential for correct operation.
3. When a user interacts with a Gradio component (e.g., clicks a button), the corresponding wrapper function (e.g., add_wrapper) is called.
4. The wrapper function calls send_command, which constructs a JSON request and sends it to Go via queue.put(). The send_command function blocks while waiting for a response.
5. The Go program's message handling loop receives the JSON request, extracts the command name, and looks up the corresponding Go function in a map.
6. The Go function is called dynamically, with the data from the JSON request converted to the appropriate Go types.
7. The Go program sends the result (or error) back to Python as a JSON response.
8. The Python send_command function receives the response (using the matchingrequest_id) and returns the result to the Gradio wrapper function.
9. The Gradio wrapper function updates the UI with the result.
10. The Go program handles an exit signal, and waits for the python process.

## Key Features Demonstrated:

- Seamless Go/Python Integration: Call Go functions directly from Gradio UI elements.
- Bidirectional Communication: Data flows in both directions between Go and Python.
- JSON-Based Messaging: Uses a simple and robust JSON format for communication.
- Dynamic Function Calling: Go functions are called dynamically based on the command received from Python.
- Concurrency: Uses goroutines for non-blocking I/O and background processing.
- Error Handling: Handles errors gracefully on both sides.
- Gradio Queueing: Leverages Gradio's built-in queueing for reliable event handling.
- Graceful Exit: The quit button and exit signal allows the program to exit gracefully.
