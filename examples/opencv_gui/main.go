package main

import (
	"bufio"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	jumpboot "github.com/richinsley/jumpboot"
)

//go:embed modules/opencv_processor.py
var opencvProcessorScript string

const (
	width         = 640
	height        = 480
	channels      = 4
	frameSize     = width * height * channels
	fps           = 30                // Target frames per second
	frameDuration = time.Second / fps // Duration between frames
	initialMode   = "edges"           // Initial processing mode
)

type AppState struct {
	processingMode string
	running        bool
	mutex          sync.RWMutex
	stopChan       chan struct{} // Channel to signal goroutine to stop
}

func main() {
	// --- Setup Jumpboot Environment ---
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "..", "environments") // Correct path
	fmt.Println("Creating Jumpboot env at: ", rootDirectory)
	env, err := jumpboot.CreateEnvironmentMamba("opencv_gui_env", rootDirectory, "3.12", "conda-forge", nil)
	if err != nil {
		log.Fatalf("Failed to create environment: %v", err)
	}
	if env.IsNew {
		fmt.Println("Installing OpenCV and numpy...")
		// OpenCV and numpy MUST be installed via conda, not pip.
		err = env.MicromambaInstallPackage("py-opencv", "conda-forge")
		if err != nil {
			log.Fatal(err)
		}
		err = env.MicromambaInstallPackage("numpy", "conda-forge")
		if err != nil {
			log.Fatal(err)
		}
	}

	// --- Shared Memory Setup ---
	frameShm, frameSize, err := jumpboot.CreateSharedNumPyArray[byte]("frame_data", []int{height, width, channels}) // Use CreateSharedNumPyArray.
	if err != nil {
		log.Fatal(err)
	}
	defer frameShm.Close()
	fmt.Printf("Shared memory size: %d\n", frameSize)

	// Get the actual name of the shared memory
	sharedMemName := frameShm.Name

	// --- Python Process Setup ---
	program := &jumpboot.PythonProgram{
		Name: "OpenCVProcessor",
		Path: cwd,
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   filepath.Join(cwd, "modules", "opencv_processor.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(opencvProcessorScript)),
		},
		// KVPairs are available to the Python program via the jumpboot module:
		// jumpboot.SHARED_MEMORY_NAME
		// jumpboot.SHARED_MEMORY_SIZE
		// jumpboot.SEMAPHORE_NAME
		KVPairs: map[string]interface{}{
			"SHARED_MEMORY_NAME": sharedMemName,
			"SHARED_MEMORY_SIZE": frameSize,          // Pass the size of the shared memory
			"SEMAPHORE_NAME":     "/frame_semaphore", // Pass the name of the semaphore
		},
	}

	pyProcess, _, err := env.NewPythonProcessFromProgram(program, nil, nil, false)
	if err != nil {
		log.Fatal(err)
	}
	defer pyProcess.Terminate()

	// --- Start Error Reading Goroutine IMMEDIATELY ---
	// This is needed in case the Python program fails to start.
	errChan := make(chan error, 1) // Buffered channel to hold one error
	go func() {
		_, err := io.Copy(os.Stderr, pyProcess.Stderr) // Copy to os.Stderr for immediate visibility
		if err != nil && err != io.EOF {
			errChan <- fmt.Errorf("error reading from Python stderr: %w", err)
		}
	}()

	// --- Check for Early Python Errors ---
	select {
	case err := <-errChan:
		log.Fatalf("Python process error: %v", err)
	case <-time.After(5 * time.Second): // Increased timeout
		// No error, proceed with GUI setup
		fmt.Println("No error detected from python start")
	}

	// --- GUI Setup (Fyne) ---
	a := app.New()
	w := a.NewWindow("OpenCV GUI")

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	imgDisplay := canvas.NewImageFromImage(img)
	imgDisplay.SetMinSize(fyne.NewSize(float32(width), float32(height)))
	imgDisplay.FillMode = canvas.ImageFillContain
	content := container.NewVBox(imgDisplay)

	// --- Application State ---
	appState := AppState{
		processingMode: initialMode,
		running:        true,
		stopChan:       make(chan struct{}),
	}

	// --- Command Sending ---
	writer := pyProcess.PipeOut
	reader := bufio.NewReader(pyProcess.PipeIn)
	sendCommand := func(command string, data interface{}) (string, error) {
		request := map[string]interface{}{
			"command": command,
			"data":    data,
		}
		requestJSON, _ := json.Marshal(request)
		_, err := writer.Write(append(requestJSON, '\n'))
		if err != nil {
			return "", fmt.Errorf("failed to write to Python: %w", err)
		}
		responseJSON, err := reader.ReadBytes('\n')
		if err != nil {
			return "", fmt.Errorf("failed to read from Python: %w", err)
		}
		return string(responseJSON), err
	}

	// --- Control Buttons ---
	controls := container.NewHBox(
		widget.NewButton("Edges", func() {
			appState.mutex.Lock()
			appState.processingMode = "edges"
			appState.mutex.Unlock()
			_, err := sendCommand("set_mode", "edges")
			if err != nil {
				log.Printf("Error sending command: %v", err)
			}
		}),
		widget.NewButton("Blur", func() {
			appState.mutex.Lock()
			appState.processingMode = "blur"
			appState.mutex.Unlock()
			_, err := sendCommand("set_mode", "blur")
			if err != nil {
				log.Printf("Error sending command: %v", err)
			}
		}),
		widget.NewButton("Gray", func() {
			appState.mutex.Lock()
			appState.processingMode = "gray"
			appState.mutex.Unlock()
			_, err := sendCommand("set_mode", "gray")
			if err != nil {
				log.Printf("Error sending command: %v", err)
			}
		}),
	)

	content.Add(controls)
	w.SetContent(content)

	// --- Frame Update Loop (in a goroutine) ---
	go func() {
		newImg := image.NewRGBA(image.Rect(0, 0, width, height))
		frameSlice := frameShm.GetByteSlice(0)
		ticker := time.NewTicker(frameDuration)
		defer ticker.Stop()

		res, err := sendCommand("howdy", nil)
		if err != nil {
			log.Printf("Error sending capture command: %v", err)
			return
		}
		fmt.Println("Sent howdy command: ", string(res))

		for {
			select {
			case <-appState.stopChan:
				fmt.Println("Frame processing goroutine stopping")
				return
			case <-ticker.C:
				if !appState.running {
					continue
				}

				// Send command to capture frame
				if _, err := sendCommand("capture_frame", nil); err != nil {
					log.Printf("Error sending capture command: %v", err)
					time.Sleep(10 * time.Millisecond)
					continue
				}

				appState.mutex.RLock()
				copy(newImg.Pix, frameSlice)
				appState.mutex.RUnlock()

				imgDisplay.Image = newImg
				imgDisplay.Refresh()
			}
		}
	}()

	// --- Window Close Handling ---
	w.SetCloseIntercept(func() {
		fmt.Println("Window close requested")

		// Stop the frame processing goroutine
		appState.mutex.Lock()
		appState.running = false
		close(appState.stopChan)
		appState.mutex.Unlock()

		// Tell Python to exit
		_, err := sendCommand("exit", nil)
		if err != nil {
			log.Printf("Error sending exit command: %v", err)
		}

		pyProcess.Wait()
		fmt.Println("Python process exited.")

		w.Close()
		fmt.Println("Window closed")
	})

	w.ShowAndRun()
	fmt.Println("Application exiting")
}
