package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

func main() {
	// Specify the binary folder to place micromamba in
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	fmt.Println("Creating Jumpboot repo at: ", rootDirectory)
	version := "3.10"
	env, err := jumpboot.CreateEnvironmentMamba("myenv"+version, rootDirectory, version, "conda-forge", jumpboot.ShowVerbose)
	if err != nil {
		fmt.Printf("Error creating environment: %v\n", err)
		return
	}
	// check if this is a new jumpboot environment and install packages
	if env.IsNew {
		// we'll install debugpy as an example (we could also use pip)
		err = env.MicromambaInstallPackage("debugpy", "conda-forge")
		if err != nil {
			fmt.Printf("Error installing debugpy: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("Created environment: %s\n", env.Name)

	// Create a Python process with unbuffered stdin/stdout
	// The script reads from stdin and writes to stdout
	main_script := `
import sys

# print the args
print(sys.argv)

while True:
    print("Waiting for input...", file=sys.stderr)
    line = sys.stdin.readline()
    if not line:  # EOF
        break
    line = line.strip()
    if line == "exit":
        break
    print(f"Processed: {line.upper()}")

print("Python script ending")
`

	binpath := filepath.Join(cwd, "bin")

	// Define a Python program with a main program
	// we could add additional embedded modules and packages here
	program := &jumpboot.PythonProgram{
		Name: "SimpleProgram",
		Path: binpath,
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   filepath.Join(binpath, "main.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(main_script)),
		},
		Modules: []jumpboot.Module{},
	}

	// create a string map of env options
	envOptions := map[string]string{
		// "PYTHONNOFROZENMODULES": "1",
	}

	pyProcess, _, err := env.NewPythonProcessFromProgram(program, envOptions, nil, false)
	if err != nil {
		panic(err)
	}

	// Read from stderr
	go func() {
		reader := bufio.NewReader(pyProcess.Stderr)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					fmt.Println("Error reading stderr:", err)
				}
				break
			}
			fmt.Print("Python stderr: ", line)
		}
	}()

	// Example of sending input to the Python script
	io.WriteString(pyProcess.Stdin, "Test input\n")
	io.WriteString(pyProcess.Stdin, "exit\n")

	// Copy stdout and stderr output from the Python script
	// to the golang process stdout and stderr
	go func() {
		io.Copy(os.Stdout, pyProcess.Stdout)
	}()
	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	// Wait for the Python process to finish
	pyProcess.Cmd.Wait()
}
