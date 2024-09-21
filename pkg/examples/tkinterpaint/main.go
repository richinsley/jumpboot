package main

import (
	"bufio"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

//go:embed modules/main.py
var main_program string

func main() {
	// Specify the binary folder to place micromamba in
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	fmt.Println("Creating Jumpboot repo at: ", rootDirectory)
	version := "3.12"
	env, err := jumpboot.CreateEnvironmentMamba("myenv"+version, rootDirectory, version, "conda-forge", nil)
	if err != nil {
		fmt.Printf("Error creating environment: %v\n", err)
		return
	}
	fmt.Printf("Created environment: %s\n", env.Name)

	if env.IsNew {
		fmt.Println("Created a new environment")
	}

	env.PipInstallPackage("bson", "", "", false, nil)
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
