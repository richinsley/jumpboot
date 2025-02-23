# JumpBoot OpenCV GUI Integration

This example demonstrates how to integrate OpenCV computer vision functions (written in Python) with a graphical user interface (GUI) built using the Fyne toolkit in Go. It showcases the use of shared memory for efficient data transfer between Go and Python, and demonstrates how to send commands and receive responses using JSON messages.

## Functionality

* **GUI (Go):**
    * Displays a live video feed from the default camera.
    * Provides buttons to switch between different OpenCV processing modes ("Edges", "Blur", "Gray").
    * Uses Fyne for the GUI elements.
    * Sends commands to the Python process to capture frames and set processing modes.
    * Receives processed frames from Python via shared memory.
    * Handles window close events to gracefully stop Python and clean up resources.
* **Python Script (`modules/opencv_processor.py`):**
    * Captures frames from the camera using OpenCV (`cv2`).
    * Processes frames based on the selected mode (edge detection, blur, grayscale).
    * Writes processed frames to shared memory.
    * Receives commands from Go via JSON messages.
    * Sends responses to Go via JSON messages.
    * Handles the "exit" command to close the camera and release resources.
* **Shared Memory:** Used for zero-copy transfer of image frames between Go and Python.
* **JSON Messaging:** Used for sending commands (Go to Python) and responses (Python to Go).

## How it Works

1.  **Environment Setup:** The Go program creates or reuses a Python environment and installs the necessary packages (`opencv-python`, `numpy`).
2.  **Shared Memory Allocation:** The Go program allocates a shared memory segment using `CreateSharedNumPyArray` to hold image frames.
3.  **Python Process Start:** The Go program starts the Python script (`modules/opencv_processor.py`). The shared memory name and a semaphore name are passed to Python as key-value pairs (accessible via the `jumpboot` module in Python).
4.  **GUI Initialization:** The Go program initializes the Fyne GUI, sets up the image display, and starts a goroutine to continuously update the displayed image.
5.  **Command/Response Loop:**
    * The Go program sends commands to Python using the `sendCommand` function (which writes JSON messages to Python's standard input).
    * The Python script receives and processes these commands in its main loop, sending back responses as JSON messages.
6.  **Frame Processing:**
    * The Python script captures frames from the camera.
    * It processes the frames based on the current mode (set by commands from Go).
    * It converts the processed frames from BGR (OpenCV's default) to RGBA (Fyne's expected format).
    * It writes the RGBA frame data to the shared memory segment.
    * The Go program's frame update loop periodically copies data from shared memory into the Fyne image and refreshes the display.
7.  **Exit Handling:**
    * When the Go program's window is closed, it sends an "exit" command to Python.
    * The Python script receives this command, releases the camera and shared memory, and exits.
    * The Go program waits for the Python process to exit before cleaning up and exiting itself.

## Prerequisites

* Go (version 1.18 or later)
* Fyne (install with `go get fyne.io/fyne/v2`)

## Running the Example
MacOS/Linux:
```bash
cd examples/opencv_gui
go mod tidy
go run main.go
```
Windows (Powershell): 
```powershell
cd examples\opencv_gui
go mod tidy
# Fyne requires CGO on Windows :(
$env:CGO_ENABLED = "1"
go run main.go
```

A window will appear displaying the live camera feed with edge detection applied. You can click the buttons to switch between processing modes.

## Key Files

* `main.go`: The main Go program that handles the GUI and communication with Python.
* `modules/opencv_processor.py`: The Python script that uses OpenCV to process frames.

## Notes

* This example demonstrates efficient communication between Go and Python using shared memory and JSON messaging.
* It provides a basic framework for integrating OpenCV computer vision capabilities into Go GUIs.
* You can extend this example to build more complex applications with different OpenCV functions and UI elements.