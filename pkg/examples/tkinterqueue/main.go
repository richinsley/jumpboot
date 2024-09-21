package main

import (
	"bufio"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"time"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

//go:embed modules/main.py
var main_program string

func main() {
	// Specify the binary folder to place micromamba in
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	fmt.Println("Creating Jumpboot repo at: ", rootDirectory)
	version := "3.11"

	progressFunc := func(message string, current, total int64) {
		if total > 0 {
			fmt.Printf("\r%s: %.2f%%", message, float64(current)/float64(total)*100)
		} else {
			fmt.Printf("\r%s: %d", message, current)
		}
	}

	env, err := jumpboot.CreateEnvironmentMamba("myenv"+version, rootDirectory, version, "conda-forge", progressFunc)
	if err != nil {
		log.Fatalf("Error creating environment: %v", err)
	}
	fmt.Printf("Created environment: %s\n", env.Name)

	if env.IsNew {
		fmt.Println("Created a new environment")
	}

	env.PipInstallPackage("bson", "", "", false, progressFunc)
	program := &jumpboot.PythonProgram{
		Name: "MyProgram",
		Path: cwd,
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   path.Join(cwd, "modules", "main.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(main_program)),
		},
		Modules:  []jumpboot.Module{},
		Packages: []jumpboot.Package{},
	}

	// create a string map of env options to pass to the Python process
	envOptions := map[string]string{}

	pyProcess, _, err := env.NewPythonProcessFromProgram(program, envOptions, nil, false)
	if err != nil {
		panic(err)
	}

	// copy output from the Python script
	go func() {
		io.Copy(os.Stdout, pyProcess.Stdout)
	}()

	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	// use a go routine to write a message to the Python process
	// at 1 second intervals
	go func() {
		for {
			// create a json string to send to the Python process
			jsonString := `{"message": "Hello from Go!"}`
			_, err = pyProcess.PipeOut.Write([]byte(jsonString + "\n"))
			if err != nil {
				fmt.Println("Error writing to Python process: ", err)
			}
			// sleep for 1 second
			time.Sleep(1 * time.Second)
		}
	}()

	// read a line from the Python process PipeIn
	for {
		b, err := bufio.NewReader(pyProcess.PipeIn).ReadBytes('\n')
		if err != nil {
			fmt.Println("Error reading from Python process: ", err)
		}
		fmt.Println("Python process says: ", string(b))
	}

	// // create a json string to send to the Python process
	// jsonString := `{"message": "Hello from Go!"}`
	// _, err = pyProcess.PipeOut.Write([]byte(jsonString + "\n"))
	// if err != nil {
	// 	fmt.Println("Error writing to Python process: ", err)
	// }

	// Wait for the Python process to finish
	pyProcess.Cmd.Wait()
}
