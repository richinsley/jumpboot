package pkg

import (
	"bufio"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
)

//go:embed scripts/repl.py
var replScript string

type REPLPythonProcess struct {
	*PythonProcess
	m              sync.Mutex
	closed         bool
	combinedOutput bool
}

func (env *Environment) NewREPLPythonProcess(environment_vars map[string]string) (*REPLPythonProcess, error) {
	cwd, _ := os.Getwd()
	program := &PythonProgram{
		Name: "JumpBootREPL",
		Path: cwd,
		Program: Module{
			Name:   "__main__",
			Path:   path.Join(cwd, "modules", "main.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(replScript)),
		},
		Modules:  []Module{},
		Packages: []Package{},
		KVPairs:  map[string]interface{}{},
		// KVPairs:  map[string]interface{}{"SHARED_MEMORY_NAME": name, "SHARED_MEMORY_SIZE": size, "SEMAPHORE_NAME": semaphore_name},
	}

	process, _, err := env.NewPythonProcessFromProgram(program, environment_vars, nil, false)
	if err != nil {
		return nil, err
	}

	return &REPLPythonProcess{
		PythonProcess:  process,
		closed:         false,
		combinedOutput: true, // the default is to combine stdout and stderr
	}, nil
}

// Define the custom delimiter with non-visible ASCII characters
const DELIMITER = "\x01\x02\x03\n"

func (rpp *REPLPythonProcess) Execute(code string, combinedOutput bool) (string, error) {
	// we need to lock the mutex to prevent multiple goroutines from writing to the Python process at the same time
	rpp.m.Lock()
	defer rpp.m.Unlock()

	// check if the Python process has been closed
	if rpp.closed {
		return "", fmt.Errorf("REPL process has been closed")
	}

	// if we are changing the combined output setting, update the Python process
	if rpp.combinedOutput != combinedOutput {
		cc := "__CAPTURE_COMBINED__ ="
		if combinedOutput {
			cc += " True" + DELIMITER
		} else {
			cc += " False" + DELIMITER
		}
		_, err := rpp.PythonProcess.PipeOut.WriteString(cc)
		if err != nil {
			return "", err
		}
		rpp.combinedOutput = combinedOutput
	}

	// trim whitespace from the end of the code
	code = strings.TrimRight(code, " \t\n\r")

	// append the DELIMITER to the end of the code
	code += DELIMITER

	// write the code to the Python process as a single string
	_, err := rpp.PythonProcess.PipeOut.WriteString(code)
	if err != nil {
		return "", err
	}

	// Read the output from Python and process it until we encounter the delimiter
	reader := bufio.NewReader(rpp.PythonProcess.PipeIn)
	var result strings.Builder

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}

		result.WriteString(line)

		// Check if we've received the complete output (marked by the delimiter)
		if strings.HasSuffix(result.String(), DELIMITER) {
			// Trim the delimiter and any trailing newline/carriage return from the output
			output := strings.TrimSuffix(result.String(), DELIMITER)
			output = strings.TrimRight(output, "\n\r")
			return output, nil
		}

		if err == io.EOF {
			return "", fmt.Errorf("unexpected EOF")
		}
	}
}

func (rpp *REPLPythonProcess) Close() error {
	// we need to lock the mutex to prevent multiple goroutines from writing to the Python process at the same time
	rpp.m.Lock()
	defer rpp.m.Unlock()

	// check if the Python process has been closed
	if rpp.closed {
		return fmt.Errorf("REPL process has been closed")
	}

	// _, err := rpp.PythonProcess.PipeOut.WriteString("exit\n")
	// if err != nil {
	// 	return err
	// }

	return rpp.PythonProcess.Terminate()
}
