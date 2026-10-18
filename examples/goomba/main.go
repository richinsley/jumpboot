// Command goomba is the repl example on an environment created with the
// goomba backend: jumpboot resolves and installs the conda packages itself,
// in Go, without downloading or running micromamba.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/richinsley/goomba/solve"
	jumpboot "github.com/richinsley/jumpboot"
)

func main() {
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	version := "3.12"

	// Ctrl-C cancels the creation; goomba then removes what it installed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Println("Creating jumpboot Python environment with goomba")
	last := ""
	progress := func(message string, current, total int64) {
		if message != last {
			last = message
			fmt.Println(message)
		}
	}
	env, err := jumpboot.CreateEnvironmentGoombaWithOptions(ctx, "goombaenv", rootDirectory, jumpboot.GoombaOptions{
		PythonVersion: version,
		Channels:      []string{"conda-forge"},
		Timeout:       15 * time.Minute,
	}, progress)
	var unsolvable *solve.Unsolvable
	switch {
	case errors.As(err, &unsolvable):
		log.Fatalf("The packages cannot be installed together:\n%s", unsolvable.Explanation)
	case errors.Is(err, jumpboot.ErrIncompleteEnvironment):
		log.Fatalf("%v\nRemove %s and run again.", err, filepath.Join(rootDirectory, "envs", "goombaenv"))
	case err != nil:
		log.Fatalf("Failed to create environment: %v", err)
	}
	fmt.Printf("Python %s, pip %s at %s (backend %s, new: %v)\n",
		env.PythonVersion.String(), env.PipVersion.String(), env.EnvPath, env.Backend, env.IsNew)

	repl, err := env.NewREPLPythonProcess(nil, nil, nil, nil)
	if err != nil {
		log.Fatalf("Failed to create REPL: %v", err)
	}
	defer repl.Close()

	// copy output from the Python script
	go func() {
		io.Copy(os.Stdout, repl.PythonProcess.Stdout)
	}()
	go func() {
		io.Copy(os.Stderr, repl.PythonProcess.Stderr)
	}()

	for _, code := range []string{
		"2 + 2",
		"print('Hello, World!')",
		"import math; math.pi",
		"import sys; sys.prefix",
		"print(1 / 0)",
	} {
		result, err := repl.Execute(code, true)
		if err != nil {
			fmt.Printf("Error executing code: %v\n", err)
		}
		fmt.Printf(">>> %s\n%s\n", code, result)
	}
}
