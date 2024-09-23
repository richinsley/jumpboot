package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

func main() {
	env, err := jumpboot.CreateEnvironmentFromSystem(nil)
	if err != nil {
		fmt.Printf("Error creating environment: %v\n", err)
		return
	}
	repl, _ := env.NewREPLPythonProcess(nil)
	defer repl.Close()

	// copy output from the Python script
	go func() {
		io.Copy(os.Stdout, repl.PythonProcess.Stdout)
	}()

	go func() {
		io.Copy(os.Stderr, repl.PythonProcess.Stderr)
	}()

	var result string

	result, err = repl.Execute("2 + 2", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: 4

	result, err = repl.Execute("print('Hello, World!')", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: Hello, World!

	result, err = repl.Execute("import math; math.pi", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: 3.141592653589793

	result, err = repl.Execute("ixvar = 2.0", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: ""

	result, err = repl.Execute("print(ixvar)", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: 2.0

	result, err = repl.Execute("print(1 / 0)", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: Traceback...

	pscript := `
for i in range(1, 11): 
	print(i)
`

	// pscript := `a = 3
	// b = 5
	// print(a + b)
	// `

	// turn off combined output and print howdy
	result, err = repl.Execute("print(ixvar)", false)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: ""

	// turn on combined output and print howdy
	result, err = repl.Execute("print('howdy')", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: howdy

	// turn on combined output and print howdy
	result, err = repl.Execute(pscript, true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result) // Output: howdy
}
