package main

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

func GetMicromambaShell(env *jumpboot.Environment) *exec.Cmd {
	// get the shell command
	args := []string{"run", "-p", env.EnvPath}

	switch runtime.GOOS {
	case "windows":
		shellPath := os.Getenv("COMSPEC")
		if shellPath == "" {
			// Default fallback on Windows if COMSPEC is not set
			shellPath = "cmd.exe"
		}
		args = append(args, shellPath)
	default:
		shellPath := os.Getenv("SHELL")
		if shellPath == "" {
			// Default fallback on Unix-like systems if SHELL is not set
			shellPath := "/bin/sh"

			// check if shellpath exists
			if _, err := exec.LookPath(shellPath); err != nil {
				// Default fallback on Unix-like systems if SHELL is not set
				shellPath = "/usr/bin/sh"
			}

			args = append(args, shellPath)
		} else {
			args = append(args, shellPath)
		}

		// add additional arguments for bash et al
		if strings.HasSuffix(shellPath, "bash") {
			// prevent bash from reading the user's .bashrc and .bash_profile
			// in case conda is installed
			args = append(args, "--noprofile", "--norc")
		}
	}

	cmd := exec.Command(env.MicromambaPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Explicitly copy current environment variables
	cmd.Env = os.Environ()

	return cmd
}

func main() {
	// Specify the binary folder to place micromamba in
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	fmt.Println("Creating Jumpboot repo at: ", rootDirectory)

	progressFunc := func(message string, current, total int64) {
		if total > 0 {
			fmt.Printf("\r%s: %.2f%%\n", message, float64(current)/float64(total)*100)
		} else {
			fmt.Printf("\r%s: %d\n", message, current)
		}
	}

	version := "3.11"
	baseEnv, err := jumpboot.CreateEnvironmentMamba("myenv"+version, rootDirectory, version, "conda-forge", progressFunc)
	if err != nil {
		log.Fatalf("Error creating environment: %v", err)
	}
	fmt.Printf("Created environment: %s\n", baseEnv.Name)

	cmd := GetMicromambaShell(baseEnv)
	if err := cmd.Run(); err != nil {
		panic(err)
	}
}
