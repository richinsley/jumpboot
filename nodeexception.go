package jumpboot

import "encoding/json"

// NodeException represents an error raised inside a Node.js process and
// reported to Go over the status pipe. It is the Node.js counterpart of
// PythonException.
type NodeException struct {
	// Exception is the error type/name (e.g., "TypeError").
	Exception string `json:"exception"`

	// Message is the error message.
	Message string `json:"message"`

	// Traceback is the JavaScript stack trace, when available.
	Traceback string `json:"traceback"`
}

// NewNodeExceptionFromJSON decodes a NodeException from a status-pipe JSON line.
func NewNodeExceptionFromJSON(data []byte) (*NodeException, error) {
	var ex NodeException
	if err := json.Unmarshal(data, &ex); err != nil {
		return nil, err
	}
	return &ex, nil
}

// Error implements the error interface.
func (e *NodeException) Error() string {
	if e.Message == "" {
		return e.Exception
	}
	return e.Exception + ": " + e.Message
}

// ToString returns a human-readable description including the stack trace.
func (e *NodeException) ToString() string {
	s := e.Exception + ": " + e.Message
	if e.Traceback != "" {
		s += "\n" + e.Traceback
	}
	return s
}
