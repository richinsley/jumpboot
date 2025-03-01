package main

import (
	_ "embed"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	jumpboot "github.com/richinsley/jumpboot"
)

func main() {
	cwd, _ := os.Getwd()
	rootDirectory := filepath.Join(cwd, "..", "environments")
	version := "3.12"

	fmt.Println("Creating jumpboot Python environment")
	env, err := jumpboot.CreateEnvironmentMamba("myenv", rootDirectory, version, "conda-forge", nil)
	if err != nil {
		log.Fatalf("Failed to create environment: %v", err)
	}

	repl, err := env.NewREPLPythonProcess(nil, nil, nil, nil)
	if err != nil {
		log.Fatalf("Failed to create REPL: %v", err)
		os.Exit(1)
	}
	defer repl.Close()

	// copy output from the Python script
	go func() {
		io.Copy(os.Stdout, repl.PythonProcess.Stdout)
		fmt.Println("Done copying stdout")
	}()

	go func() {
		io.Copy(os.Stderr, repl.PythonProcess.Stderr)
		fmt.Println("Done copying stderr")
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
	}
	fmt.Println(result) // Output: Traceback...

	pscript := `
for i in range(1, 11): 
	print(i)
`

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

	factors := `
def factors(n):
    if n < 1:
        return "Factors are only defined for positive integers"
    
    factor_list = []
    for i in range(1, int(n**0.5) + 1):
        if n % i == 0:
            factor_list.append(i)
            if i != n // i:
                factor_list.append(n // i)
    
    return sorted(factor_list)
`
	// give the factor function to the python interpreter
	result, err = repl.Execute(factors, true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result)

	// we can now call the factors function from the python interpreter as many times as we want
	// calculate the factorial of of all the numbers from 1 to 1000
	for i := 1; i <= 1000; i++ {
		result, err = repl.Execute(fmt.Sprintf("factors(%d)", i), true)
		if err != nil {
			fmt.Printf("Error executing code: %v\n", err)
			return
		}
		fmt.Printf("factorial(%d) = %s\n", i, result)
	}

	// create a python function that loops forever and sleeps for 1 second each iteration
	// this will cause the python interpreter to hang until we kill the process
	forever := `
import time
def forever():
	while True:
		print("Sleeping for 1 second")
		time.sleep(1)
`
	// give the forever function to the python interpreter
	// repl.Execute("import time", false)
	result, err = repl.Execute(forever, true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
	fmt.Println(result)

	// call the forever function with a timeout of 3 seconds
	result, err = repl.ExecuteWithTimeout("forever()", true, 3*time.Second)
	if err != nil {
		// this is expected because the python interpreter is hanging
		fmt.Printf("%v\n", err)
	}
	fmt.Println(result)

	// now say goodbye from python - it will return an error because the python interpreter is closed because of the timeout
	result, err = repl.Execute("print('Goodbye!')", true)
	if err != nil {
		fmt.Printf("Error executing code: %v\n", err)
		return
	}
}
