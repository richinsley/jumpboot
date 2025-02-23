# JumpBoot ChromaDB Integration Example

This example demonstrates how to integrate ChromaDB, a vector database, into a Go application using the JumpBoot library. It shows how to perform common ChromaDB operations (creating a collection, adding documents, querying) from Go by interacting with a Python process.

## Functionality

*   **Environment Setup:**  Creates a Python environment using `micromamba` and installs `chromadb` and `tiktoken` (required by ChromaDB) via pip.
*   **Python Script (`chromadb_app.py`):**
    *   Uses the `chromadb` library to create an in-memory ChromaDB client.
    *   Listens for commands (JSON messages) from the Go program via standard input.
    *   Handles the following commands:
        *   `create_collection`: Creates a new collection.
        *   `add_documents`: Adds documents, metadata, and IDs to a collection.
        *   `query`: Performs a similarity search using query text.
        *   `get`: Retrieves documents by their IDs.
        *   `exit`:  Gracefully exits the Python process.
    *   Sends responses (JSON messages) back to the Go program via standard output.
*   **Go Program (`main.go`):**
    *   Manages the Python environment and process using JumpBoot.
    *   Sends commands to the Python script.
    *   Receives and prints the results from the Python script.
    *   Handles graceful shutdown.
*   **Communication:** Uses JSON messages over standard input/output pipes for communication between Go and Python.

## How it Works

1.  **Environment Creation:** The Go program uses JumpBoot's `CreateEnvironmentMamba` function to create a new, isolated Python environment. If the environment already exists, it reuses it.  It then uses `PipInstallPackages` to install the necessary Python packages.

2.  **Python Process Start:** The Go program launches the `chromadb_app.py` script as a subprocess using JumpBoot's `NewPythonProcessFromProgram`. This establishes communication pipes between the Go program and the Python process.

3.  **Command/Response Loop:**
    *   The Go program sends commands to the Python script by constructing JSON messages and writing them to the Python process's standard input.
    *   The Python script uses `jumpboot.JSONQueue` to receive and parse these JSON messages.
    *   The `handle_command` function in the Python script processes the command (using the `chromadb` library) and constructs a JSON response.
    *   The Python script sends the JSON response back to the Go program via its standard output using `jumpboot.JSONQueue`.
    *   The Go program reads the response from Python's standard output, parses the JSON, and processes the results (e.g., printing them to the console).

4.  **Exit Handling:** The Go program sends an "exit" command to the Python script. The Python script receives this command, prints a message, sends an empty JSON response, and then exits. The Go program waits for the python process to exit using `pyProcess.Wait`.

## Prerequisites

*   Go (version 1.18 or later)
*   micromamba (Jumpboot will download it automatically if it's not found)

## Running the Example

The program will:

*   Create a Python environment (if it doesn't exist).
*   Install `chromadb` (if it's not already installed).
*   Start the Python script.
*   Create a ChromaDB collection named "my_collection".
*   Add three documents to the collection.
*   Perform a similarity search using the query text "document".
*   Print the query results to the console.
*  Retrieve the documents by their IDs.
*   Exit gracefully.

You'll see output in the console indicating the progress and results of each step.

## Key Files

*   **`main.go`:** The main Go program that orchestrates the interaction with ChromaDB.
*   **`modules/chromadb_app.py`:** The Python script that uses the `chromadb` library and handles commands from the Go program.

## Notes

*   This example uses an *in-memory* ChromaDB instance.  Data will not persist after the Python process exits.
*   Error handling is included to make the example more robust.
*   The use of a separate Python process and communication via pipes provides isolation and avoids the need for CGO for the core ChromaDB interaction.
*   The communication protocol is a simple command/response pattern using JSON messages.

This example provides a starting point for building more complex Go applications that leverage ChromaDB and other Python libraries.  You can easily extend this example to support more ChromaDB features or integrate with other Go libraries and frameworks.