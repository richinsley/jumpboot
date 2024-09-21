package main

import (
	_ "embed"
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

	// create a string map of env options to pass to the Python process
	envOptions := map[string]string{}

	pyproc, err := env.NewPythonExecProcess(envOptions, nil)
	if err != nil {
		fmt.Println("Error creating Python process: ", err)
		return
	}

	go func() {
		io.Copy(os.Stdout, pyproc.Stdout)
	}()

	// retv, err := pyproc.Exec("print('Hello from Python!')")
	code := `
# print the available variables in the current scope
# https://www.geeksforgeeks.org/exec-in-python/
from math import *
print(dir())
`
	retv, err := pyproc.Exec(code)
	if err != nil {
		fmt.Println("Error executing Python code: ", err)
		os.Exit(1)
	}
	fmt.Println("Python process returned: ", string(retv))

	code = `
import json
print(json.dumps(pow(2, 3)))
`
	retv, err = pyproc.Exec(code)
	if err != nil {
		fmt.Println("Error executing Python code: ", err)
		os.Exit(1)
	}
	fmt.Println("Python process returned: ", string(retv))

	pyproc.Close()
	pyproc.Cmd.Wait()
}
