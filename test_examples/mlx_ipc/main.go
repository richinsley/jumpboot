package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

//go:embed modules/models.py
var models string

//go:embed modules/utils.py
var utils string

//go:embed modules/generate.py
var generate string

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
		// install mlx requirements for apple silicon MLX
		// mlx>=0.8
		// numpy
		// protobuf==3.20.2
		// sentencepiece
		// huggingface_hub
		requirements := []string{"mlx>=0.8", "debugpy", "numpy", "protobuf==3.20.2", "sentencepiece", "huggingface_hub"}
		err = env.PipInstallPackages(requirements, "", "", false, jumpboot.ShowNothing)
		if err != nil {
			fmt.Printf("Error installing packages: %v\n", err)
			return
		}
	}

	// the original mlx example exists as a program, not a package, so we'll load each module individually
	binpath := filepath.Join(cwd, "modules")
	utils_module := jumpboot.NewModuleFromString("utils", path.Join(binpath, "utils.py"), utils)
	models_module := jumpboot.NewModuleFromString("models", path.Join(binpath, "models.py"), models)
	generate_module := jumpboot.NewModuleFromString("generate", path.Join(binpath, "generate.py"), generate)

	program := &jumpboot.PythonProgram{
		Name: "MyProgram",
		Path: binpath,
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   path.Join(binpath, "main.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(main_program)),
		},
		Modules: []jumpboot.Module{
			*utils_module,
			*models_module,
			*generate_module,
		},
	}

	// create a string map of env options to pass to the Python process
	envOptions := map[string]string{}

	pyProcess, err := env.NewPythonProcessFromProgram(program, envOptions, nil, false)
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

	// the jumpboot program makes available a PipeIn and PipeOut in the sys module
	// write the prompt to the PipeOut
	// the python script will read from sys.Pipe_in.readline().strip()
	pyProcess.PipeOut.Write([]byte("Write a quicksort in Python\n"))

	// The bootstrap script makes available
	// Read from the primary PipeIn.
	buf := make([]byte, 1024)
	n, err := pyProcess.PipeIn.Read(buf)
	if err != nil {
		fmt.Println("Error reading from pipe: ", err)
		os.Exit(1)
	}
	fmt.Println("Read ", n, " bytes from pipe: ", string(buf))

	// Wait for the Python process to finish
	pyProcess.Cmd.Wait()
}
