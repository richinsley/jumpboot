package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"log"

	jumpboot "github.com/richinsley/jumpboot"
)

//go:embed modules/main.py
var calculatorScript string

func main() {
	// Create the Python environment
	fmt.Println("Creating Python environment...")
	env, err := jumpboot.CreateEnvironmentMamba("calculator_env", "./envs", "3.9", "conda-forge", nil)
	if err != nil {
		log.Fatal(err)
	}

	// Create the program with the calculator service
	program := &jumpboot.PythonProgram{
		Name: "CalculatorService",
		Path: "./",
		Program: jumpboot.Module{
			Name:   "__main__",
			Path:   "./calculator_service.py",
			Source: base64.StdEncoding.EncodeToString([]byte(calculatorScript)),
		},
	}

	// Create the JSON queue process
	calculator, err := env.NewJSONQueueProcess(program, nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer calculator.Close()

	// List available methods
	methods := calculator.GetMethods()
	fmt.Println("Available methods:", methods)

	// Get detailed info for a method
	addInfo, _ := calculator.GetMethodInfo("add")
	fmt.Printf("Add method: %s\n", addInfo.Doc)
	fmt.Printf("Parameters: %+v\n", addInfo.Parameters)

	// Call methods directly
	result, err := calculator.Call("add", map[string]float64{"x": 5, "y": 3})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("5 + 3 =", result)

	// Call with a list
	avgResult, err := calculator.Call("calculate_average", map[string]interface{}{
		"values": []float64{1, 2, 3, 4, 5},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Average:", avgResult)

	// Safe error handling
	_, err = calculator.Call("divide", map[string]float64{"x": 10, "y": 0})
	if err != nil {
		fmt.Println("Expected error:", err)
	}
}
