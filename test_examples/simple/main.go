package main

import (
	"bufio"
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
	env, err := jumpboot.CreateEnvironment("myenv"+version, rootDirectory, version, "conda-forge", jumpboot.ShowVerbose)
	if err != nil {
		fmt.Printf("Error creating environment: %v\n", err)
		return
	}
	fmt.Printf("Created environment: %s\n", env.Name)

	// installing mlx with micromamba
	err = env.MicromambaInstallPackage("debugpy", "conda-forge")
	if err != nil {
		fmt.Printf("Error installing debugpy: %v\n", err)
		os.Exit(1)
	}

	// Create a Python process with unbuffered stdin/stdout
	// The script reads from stdin and writes to stdout
	script := `
import sys

# print the args.  With the bootstrap loader, the first arg is "-c" and arg[1..n] are the script arguments
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

	pyProcess, err := env.NewPythonProcessFromString(script, nil, nil, false, "foo", "bar")
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

	// Read output from the Python script
	go func() {
		io.Copy(os.Stdout, pyProcess.Stdout)
	}()
	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	// Wait for the Python process to finish
	pyProcess.Cmd.Wait()
}
