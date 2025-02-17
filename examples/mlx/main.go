package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	jumpboot "github.com/richinsley/jumpboot"
)

//go:embed modules/models.py
var models string

//go:embed modules/utils.py
var utils string

//go:embed modules/generate.py
var generate string

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
		// install mlx requirements for apple silicon MLX
		// mlx>=0.8
		// numpy
		// protobuf==3.20.2
		// sentencepiece
		// huggingface_hub
		requirements := []string{"mlx>=0.8", "debugpy", "numpy", "protobuf==3.20.2", "sentencepiece", "huggingface_hub"}
		err = env.PipInstallPackages(requirements, "", "", false, nil)
		if err != nil {
			fmt.Printf("Error installing packages: %v\n", err)
			return
		}
	}

	// the original mlx example exists as a program, not a package, so we'll load each module individually
	binpath := filepath.Join(cwd, "modules")
	utils_module := jumpboot.NewModuleFromString("utils", filepath.Join(binpath, "utils.py"), utils)
	models_module := jumpboot.NewModuleFromString("models", filepath.Join(binpath, "models.py"), models)
	generate_module := jumpboot.NewModuleFromString("generate", filepath.Join(binpath, "generate.py"), generate)

	// collect the modules into a slice
	modules := []jumpboot.Module{*utils_module, *models_module, *generate_module}

	// create a new REPL Python process with the modules
	repl, _ := env.NewREPLPythonProcess(nil, nil, modules, nil)
	defer repl.Close()

	// import the modules into the Python process that we'll need
	imports := `
import generate
import models
import mlx.core as mx
import jumpboot	`
	repl.Execute(imports, true)

	// set the random seed
	repl.Execute("mx.random.seed(90909090)", true)

	// load the model and tokenizer
	repl.Execute("model, tokenizer = models.load('Mistral-7B-Instruct-v0.3.Q8_0.gguf', 'MaziyarPanahi/Mistral-7B-Instruct-v0.3-GGUF')", true)

	// set the prompt
	repl.Execute("prompt = 'Write a quicksort in Python'", true)

	// set the max tokens
	repl.Execute("max_tokens = 100", true)

	// set the temperature
	repl.Execute("temp = 0.5", true)

	fmt.Println("Generating text...")

	// use mlx to generate text
	res, err := repl.Execute("generate.generate(model, tokenizer, prompt, max_tokens, temp)", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}

	// print the result
	fmt.Println(res)
}
