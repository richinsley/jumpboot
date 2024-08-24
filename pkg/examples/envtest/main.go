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
	"strings"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

var main_program string = `
import sys
import requests
from io import BytesIO
from pydub import AudioSegment
from pydub.playback import play

# print python system information
print("\nPython Interpreter Information")
print(sys.version)
print(sys.version_info)
print(sys.executable)
# pip information
print("\nPython Package Information")
print(sys.path)
print(sys.prefix)
print(sys.base_prefix)
print(sys.exec_prefix)
print(sys.platform)
print(sys.argv)
print(sys.flags)
print(sys.float_info)
print(sys.float_repr_style)
print("exit")

# Replace with your actual URL
url = 'https://www.myinstants.com/media/sounds/dry-fart.mp3'

# Download the audio file
response = requests.get(url)
audio_data = BytesIO(response.content)

# Load the audio file into pydub
audio = AudioSegment.from_file(audio_data, format="mp3")

# Play the audio
play(audio)
`

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

	baseEnv, err := jumpboot.CreateEnvironmentFromSystem(progressFunc)
	// env, err := jumpboot.CreateEnvironment("myenv"+version, rootDirectory, version, "conda-forge", progressFunc)
	if err != nil {
		log.Fatalf("Error creating environment: %v", err)
	}
	fmt.Printf("Created environment: %s\n", baseEnv.Name)

	if baseEnv.IsNew {
		fmt.Println("Created a new environment")
	}

	// create a virtual environment from the system python
	venvOptions := jumpboot.VenvOptions{
		SystemSitePackages: true,
		Upgrade:            true,
		Prompt:             "my-venv",
		UpgradeDeps:        true,
	}
	env, err := jumpboot.CreateVenvEnvironment(baseEnv, path.Join(rootDirectory, "sysvenv"), venvOptions, progressFunc)
	if err != nil {
		log.Fatalf("Error creating venv environment: %v", err)
	}

	if env.IsNew {
		fmt.Println("Created a new venv environment")
		env.PipInstallPackage("requests", "", "", false, progressFunc)
		env.PipInstallPackage("pydub", "", "", false, progressFunc)
	}

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

	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	// Read the output line by line
	scanner := bufio.NewScanner(pyProcess.Stdout)
	var output string
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println(line)     // Print each line (optional)
		output += line + "\n" // Store the line in the output string
		if strings.HasPrefix(line, "exit") {
			break
		}
	}

	// Wait for the Python process to finish
	pyProcess.Cmd.Wait()
}
