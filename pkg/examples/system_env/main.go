package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

func main() {
	// create a jumpboot environment from the system installed Python
	system_env, err := jumpboot.CreateEnvironmentFromSystem()
	if err != nil {
		log.Fatalf("Error creating environment: %v", err)
	}

	// Specify the root folder where the environments will be created
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "environments")

	// use the system env to generate a new virtual environment
	venvOptions := jumpboot.VenvOptions{
		SystemSitePackages: true,
		Upgrade:            true,
		Prompt:             "my-venv",
		UpgradeDeps:        true,
	}
	venv, err := jumpboot.CreateVenvEnvironment(system_env, filepath.Join(rootDirectory, "sysvenv"), venvOptions, nil)
	if err != nil {
		log.Fatalf("Error creating venv environment: %v", err)
	}
	// show the version of Python
	fmt.Printf("Python version: %s\n", venv.PythonVersion.String())
	// show the location of the Python executable
	fmt.Printf("Python executable: %s\n", venv.PythonPath)
}
