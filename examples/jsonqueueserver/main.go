package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"log"
	"time"

	jumpboot "github.com/richinsley/jumpboot"
)

//go:embed modules/main.py
var calculatorScript string

type MyService struct{}

func (s *MyService) Add(x, y float64) float64 {
	return x + y
}

func (s *MyService) Greet(name string) (string, error) {
	return "Hello, " + name + "!", nil
}

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
			Path:   "/Users/richardinsley/Projects/jumpboot/jumpboot/examples/jsonqueueserver/modules/main.py",
			Source: base64.StdEncoding.EncodeToString([]byte(calculatorScript)),
		},
	}

	// Uncomment to enable debugging
	// program.DebugPort = 9898
	// program.BreakOnStart = true

	// Create an instance of your service struct
	service := &MyService{}

	// Create the JSON queue process
	calculator, err := env.NewJSONQueueProcess(program, service, nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer calculator.Close()

	// Register a handler for the "get_tax_rate" command
	calculator.RegisterHandler("get_tax_rate", func(data interface{}, requestID string) (interface{}, error) {
		// Extract the state from the data
		dataMap, ok := data.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid data format")
		}

		state, ok := dataMap["state"].(string)
		if !ok {
			return nil, fmt.Errorf("state not provided or not a string")
		}

		// Get the tax rate based on the state
		taxRate := getTaxRateForState(state)
		fmt.Printf("Go: Providing tax rate %.4f for state %s\n", taxRate, state)

		// Return the tax rate
		return map[string]interface{}{
			"rate": taxRate,
		}, nil
	})

	// List available methods
	methods := calculator.GetMethods()
	fmt.Println("Available methods:", methods)

	// Get detailed info for a method
	addInfo, _ := calculator.GetMethodInfo("add")
	fmt.Printf("Add method: %s\n", addInfo.Doc)
	fmt.Printf("Parameters: %+v\n", addInfo.Parameters)

	result, err := calculator.Call("greet", 0, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Greet:", result)

	// Call methods directly
	result, err = calculator.Call("add", 0, map[string]float64{"x": 5, "y": 3})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("5 + 3 =", result)

	// Call with a list
	avgResult, err := calculator.Call("calculate_average", 0, map[string]interface{}{
		"values": []float64{1, 2, 3, 4, 5},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Average:", avgResult)

	// Call the Python calculate_with_tax method
	fmt.Println("Calling calculate_with_tax...")
	result, err = calculator.Call("calculate_with_tax", 0, map[string]interface{}{
		"amount": 100.0,
		"state":  "CA",
	})
	if err != nil {
		log.Fatalf("Error calling calculate_with_tax: %v", err)
	}

	fmt.Printf("Total with tax: $%.2f\n", result)

	// Try another state
	result, err = calculator.Call("calculate_with_tax", 0, map[string]interface{}{
		"amount": 50.0,
		"state":  "NY",
	})
	if err != nil {
		log.Fatalf("Error calling calculate_with_tax: %v", err)
	}

	// Safe error handling
	_, err = calculator.Call("divide", 0, map[string]float64{"x": 10, "y": 0})
	if err != nil {
		fmt.Println("Expected error:", err)
	}

	// profile 1000 calls
	start := time.Now()
	for i := 0; i < 1000; i++ {
		_, err = calculator.Call("add", 0, map[string]float64{"x": float64(i), "y": 3})
		if err != nil {
			log.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("Execution time for 1000 add operations: %s\n", elapsed)

	// Call the Python calculate_with_tax method using chaining and CallReflect
	fmt.Println("Calling calculate_with_tax CallReflect...")
	var resultFloat float64
	err = calculator.
		On("calculate_with_tax").
		Do("amount", 100.0, "state", "CA").
		WithTimeout(5 * time.Second).
		CallReflect(&resultFloat)
	if err != nil {
		log.Fatalf("Error calling calculate_with_tax: %v", err)
	}
	fmt.Println("Total with tax: $", resultFloat)

	// Call the Python calculate_with_tax method using chaining and Call
	fmt.Println("Calling calculate_with_tax Call...")
	val, err := calculator.
		On("calculate_with_tax").
		Do("amount", 100.0, "state", "CA").
		WithTimeout(5 * time.Second).
		Call()
	if err != nil {
		log.Fatalf("Error calling calculate_with_tax: %v", err)
	}
	fmt.Println("Total with tax: $", val)
}

// Helper function to get tax rate for a state
func getTaxRateForState(state string) float64 {
	// Sample tax rates (in a real application, these might come from a database)
	taxRates := map[string]float64{
		"CA": 0.0725, // 7.25%
		"NY": 0.0845, // 8.45%
		"TX": 0.0625, // 6.25%
		"FL": 0.06,   // 6%
	}

	rate, ok := taxRates[state]
	if !ok {
		// Default rate if state not found
		return 0.05 // 5%
	}
	return rate
}
