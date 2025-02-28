package jumpboot

import (
	"encoding/json"
	"fmt"
)

type PythonException struct {
	Exception string `json:"exception"`
	Message   string `json:"message"`
	Traceback string `json:"traceback"`
}

func (e *PythonException) ToString() string {
	return fmt.Sprintf("%s: %s\n%s", e.Exception, e.Message, e.Traceback)
}

func (e *PythonException) Error() error {
	return fmt.Errorf("%s", e.ToString())
}

func NewPythonExceptionFromJSON(data []byte) (*PythonException, error) {
	var pyException PythonException
	err := json.Unmarshal(data, &pyException)
	if err != nil {
		return nil, err
	}
	return &pyException, nil
}
