package main

import (
	"bufio"
	"embed"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

// ensure to embed '*.py' files in the packages directory to be included in the final binary
//
//go:embed packages/math_operations/__init__.py
//go:embed packages/math_operations/*.py
var math_operations embed.FS

//go:embed packages/tabulate/*.py
var tabulate embed.FS

//go:embed modules/main.py
var main_program string

func main() {
	// Specify the binary folder to place micromamba in
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	fmt.Println("Creating Jumpboot repo at: ", rootDirectory)
	version := "3.12"
	env, err := jumpboot.CreateEnvironment("myenv"+version, rootDirectory, version, "conda-forge", jumpboot.ShowNothing)
	if err != nil {
		fmt.Printf("Error creating environment: %v\n", err)
		return
	}
	fmt.Printf("Created environment: %s\n", env.Name)

	if env.IsNew {
		fmt.Println("Created a new environment")
	}

	math_operations_package, err := jumpboot.NewPackageFromFS("math_operations", "math_operations", "packages/math_operations", math_operations)
	if err != nil {
		fmt.Printf("Error creating package: %v\n", err)
		os.Exit(1)
	}

	tabulate_package, err := jumpboot.NewPackageFromFS("tabulate", "tabulate", "packages/tabulate", tabulate)
	if err != nil {
		fmt.Printf("Error creating package: %v\n", err)
		os.Exit(1)
	}

	program := &jumpboot.PythonProgram{
		Name: "MyProgram",
		Path: cwd,
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   path.Join(cwd, "modules", "main.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(main_program)),
		},
		Modules: []jumpboot.Module{},
		Packages: []jumpboot.Package{
			*math_operations_package,
			*tabulate_package,
		},
	}

	// create a string map of env options to pass to the Python process
	envOptions := map[string]string{}

	pyProcess, program_data, err := env.NewPythonProcessFromProgram(program, envOptions, nil, false)
	if err != nil {
		panic(err)
	}

	// write the program data to a file for diagnostic purposes
	program_data_file, err := os.Create("program_data.json")
	if err != nil {
		panic(err)
	}
	program_data_file.Write(program_data)
	program_data_file.Close()

	// copy output from the Python script
	go func() {
		io.Copy(os.Stdout, pyProcess.Stdout)
	}()

	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	// read a line from the Python process PipeIn
	b, err := bufio.NewReader(pyProcess.PipeIn).ReadBytes('\n')
	if err != nil {
		fmt.Println("Error reading from Python process: ", err)
	}
	fmt.Println("Python process says: ", string(b))

	// Wait for the Python process to finish
	pyProcess.Cmd.Wait()
}
