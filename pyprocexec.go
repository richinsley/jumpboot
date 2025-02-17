package jumpboot

import (
	"bufio"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type ExecOptions struct {
	ExecType string `json:"type"`
	Command  string `json:"code"`
}

type ExecResult struct {
	ReturnType string `json:"type"`
	Output     string `json:"output"`
}

//go:embed modules/pyprocexec/main.py
var pythonExecMain string

type PythonExecProcess struct {
	*PythonProcess
}

func (env *Environment) NewPythonExecProcess(environment_vars map[string]string, extrafiles []*os.File) (*PythonExecProcess, error) {
	cwd, _ := os.Getwd()
	program := &PythonProgram{
		Name: "PythonExecProcess",
		Path: cwd,
		Program: Module{
			Name:   "__main__",
			Path:   filepath.Join(cwd, "modules", "main.py"),
			Source: base64.StdEncoding.EncodeToString([]byte(pythonExecMain)),
		},
		Modules:  []Module{},
		Packages: []Package{},
	}

	pyProcess, _, err := env.NewPythonProcessFromProgram(program, environment_vars, nil, false)
	if err != nil {
		return nil, err
	}

	return &PythonExecProcess{
		PythonProcess: pyProcess,
	}, nil
}

func (p *PythonExecProcess) Exec(code string) (string, error) {
	e := ExecOptions{
		ExecType: "exec",
		Command:  code,
	}

	// encode the command to JSON
	cmd_json, err := json.Marshal(e)
	if err != nil {
		return "", err
	}

	// send the command to the Python process
	_, err = p.PipeOut.Write([]byte(string(cmd_json) + "\n"))
	if err != nil {
		return "", err
	}

	// read the output from the Python process
	b, err := bufio.NewReader(p.PipeIn).ReadBytes('\n')
	if err != nil {
		return "", err
	}

	// decode the output from JSON
	var result ExecResult
	err = json.Unmarshal(b, &result)
	if err != nil {
		return "", err
	}

	if result.ReturnType == "error" {
		return "", errors.New(result.Output)
	} else {
		return result.Output, nil
	}
}

func (p *PythonExecProcess) Close() {
	e := ExecOptions{
		ExecType: "exit",
		Command:  "",
	}

	// encode the command to JSON
	cmd_json, _ := json.Marshal(e)
	p.PipeOut.Write([]byte(string(cmd_json) + "\n"))
}
