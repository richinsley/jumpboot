package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"log"
	"sync"
	"time"

	jumpboot "github.com/richinsley/jumpboot"
)

//go:embed modules/gui.py
var guiScript string

func main() {
	// Create the Python environment
	fmt.Println("Creating Python environment...")
	env, err := jumpboot.CreateEnvironmentMamba("gui_env", "./envs", "3.9", "conda-forge", nil)
	if err != nil {
		log.Fatal(err)
	}

	// Create the program with the GUI service
	program := &jumpboot.PythonProgram{
		Name: "GUIService",
		Path: "./",
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   "./modules/gui.py",
			Source: base64.StdEncoding.EncodeToString([]byte(guiScript)),
		},
	}

	// Create the service
	gui, err := env.NewJSONQueueProcess(program, nil, nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer gui.Close()

	// Create a WaitGroup to wait for GUI exit
	var wg sync.WaitGroup
	wg.Add(1)

	// Register a handler for window close notification
	gui.RegisterHandler("window_closed", func(data interface{}, requestID string) (interface{}, error) {
		fmt.Println("Window closed by user, shutting down...")
		wg.Done() // Signal that we can exit
		return "ok", nil
	})

	// Register handlers for button callbacks
	gui.RegisterHandler("button_clicked", func(data interface{}, requestID string) (interface{}, error) {
		dataMap, ok := data.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid data format")
		}

		buttonID, ok := dataMap["button_id"].(string)
		if !ok {
			return nil, fmt.Errorf("button_id not provided or not a string")
		}

		fmt.Printf("Button clicked: %s\n", buttonID)

		return fmt.Sprintf("Button %s was clicked at %s", buttonID, time.Now().Format(time.RFC3339)), nil
	})

	// Update the label
	_, err = gui.Call("update_label", 0, "Hello from Go!")
	if err != nil {
		log.Fatal(err)
	}

	// Create some buttons
	button1ID, err := gui.Call("create_button", 0, map[string]interface{}{
		"text":         "Click Me",
		"command_name": "button_clicked",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Created button with ID: %s\n", button1ID)

	// Set progress to 50%
	_, err = gui.Call("set_progress", 0, 50)
	if err != nil {
		log.Fatal(err)
	}

	// Start a goroutine for periodic updates
	stopChan := make(chan struct{})

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		progress := 0

		for {
			select {
			case <-ticker.C:
				progress = (progress + 10) % 100

				// Try to update the progress bar, but don't block if there's an error
				_, err = gui.Call("set_progress", 0, progress)
				if err != nil {
					fmt.Println("GUI update error:", err)
					return
				}

				_, err = gui.Call("update_result", 0, fmt.Sprintf("Last update: %s", time.Now().Format("15:04:05")))
				if err != nil {
					fmt.Println("GUI update error:", err)
					return
				}

			case <-stopChan:
				ticker.Stop()
				return
			}
		}
	}()

	// Wait for the GUI to signal it's closing
	fmt.Println("GUI running. Close the window to exit.")
	wg.Wait()

	// Stop the update goroutine
	close(stopChan)

	fmt.Println("GUI closed, exiting program.")
}
