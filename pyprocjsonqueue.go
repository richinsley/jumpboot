package jumpboot

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// JSONQueueProcess extends PythonProcess with JSON-based bidirectional communication
// that can directly call methods on Python classes with bidirectional command handling
type JSONQueueProcess struct {
	*PythonProcess
	reader          *bufio.Reader
	writer          *bufio.Writer
	mutex           sync.Mutex
	responseMap     map[string]chan map[string]interface{}
	commandHandlers map[string]CommandHandler
	defaultHandler  CommandHandler
	nextID          int64
	idMutex         sync.Mutex
	methodCache     map[string]MethodInfo
	running         bool
	processingWg    sync.WaitGroup
}

// CommandHandler defines a function type for handling commands from Python
type CommandHandler func(data interface{}, requestID string) (interface{}, error)

// RegisterHandler registers a handler for a specific command
func (jq *JSONQueueProcess) RegisterHandler(command string, handler CommandHandler) {
	jq.mutex.Lock()
	defer jq.mutex.Unlock()
	jq.commandHandlers[command] = handler
}

// SetDefaultHandler sets a handler for commands without a specific handler
func (jq *JSONQueueProcess) SetDefaultHandler(handler CommandHandler) {
	jq.mutex.Lock()
	defer jq.mutex.Unlock()
	jq.defaultHandler = handler
}

// MethodInfo represents metadata about an exposed Python method
type MethodInfo struct {
	Parameters []ParameterInfo   `json:"parameters"`
	Return     map[string]string `json:"return"`
	Doc        string            `json:"doc"`
}

// ParameterInfo represents metadata about a Python method parameter
type ParameterInfo struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Type     string `json:"type,omitempty"`
}

// NewJSONQueueProcess creates a new PythonProcess with JSON queue communication
func (env *Environment) NewJSONQueueProcess(program *PythonProgram, environment_vars map[string]string, extrafiles []*os.File) (*JSONQueueProcess, error) {
	pyProcess, _, err := env.NewPythonProcessFromProgram(program, environment_vars, extrafiles, false)
	if err != nil {
		return nil, err
	}

	// Goroutine to read Python's stdout
	go func() {
		io.Copy(os.Stdout, pyProcess.Stdout)
	}()

	// Goroutine to read Python's stderr
	go func() {
		io.Copy(os.Stderr, pyProcess.Stderr)
	}()

	jq := &JSONQueueProcess{
		PythonProcess: pyProcess,
		reader:        bufio.NewReader(pyProcess.PipeIn),
		writer:        bufio.NewWriter(pyProcess.PipeOut),
		responseMap:   make(map[string]chan map[string]interface{}),
		nextID:        1,
		methodCache:   make(map[string]MethodInfo),
	}

	// Start the message processing
	jq.Start()

	// Start the message loop
	go jq.messageLoop()

	// Fetch method info from Python
	err = jq.discoverMethods()
	if err != nil {
		// Not fatal, just log it
		fmt.Printf("Warning: Failed to discover Python methods: %v\n", err)
	}

	return jq, nil
}

// discoverMethods fetches information about exposed Python methods
func (jq *JSONQueueProcess) discoverMethods() error {
	response, err := jq.SendCommand("__get_methods__", nil, true)
	if err != nil {
		return err
	}

	methods, ok := response["methods"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid method information returned")
	}

	// Parse method info
	for name, info := range methods {
		infoMap, ok := info.(map[string]interface{})
		if !ok {
			continue
		}

		// Convert to our structure
		methodInfo := MethodInfo{
			Doc: infoMap["doc"].(string),
		}

		// Parse parameters
		if params, ok := infoMap["parameters"].([]interface{}); ok {
			for _, p := range params {
				param, ok := p.(map[string]interface{})
				if !ok {
					continue
				}

				paramInfo := ParameterInfo{
					Name:     param["name"].(string),
					Required: param["required"].(bool),
				}

				if typeName, ok := param["type"]; ok {
					paramInfo.Type = typeName.(string)
				}

				methodInfo.Parameters = append(methodInfo.Parameters, paramInfo)
			}
		}

		// Store in the cache
		jq.methodCache[name] = methodInfo
	}

	return nil
}

// Call dynamically calls a Python method by name with the provided arguments
func (jq *JSONQueueProcess) Call(methodName string, args interface{}) (interface{}, error) {
	response, err := jq.SendCommand(methodName, args, true)
	if err != nil {
		return nil, err
	}

	// Check for errors
	if errMsg, ok := response["error"].(string); ok {
		return nil, fmt.Errorf("python error: %s", errMsg)
	}

	// Return the result (might be in "result" or directly in the response)
	if result, ok := response["result"]; ok {
		return result, nil
	}

	// Return the whole response (minus request_id)
	delete(response, "request_id")
	if len(response) == 1 {
		for _, v := range response {
			return v, nil
		}
	}
	return response, nil
}

// GetMethods returns a list of exposed Python methods
func (jq *JSONQueueProcess) GetMethods() []string {
	var methods []string
	for name := range jq.methodCache {
		methods = append(methods, name)
	}
	return methods
}

// GetMethodInfo returns information about a specific method
func (jq *JSONQueueProcess) GetMethodInfo(methodName string) (MethodInfo, bool) {
	info, ok := jq.methodCache[methodName]
	return info, ok
}

// Start begins processing messages
func (jq *JSONQueueProcess) Start() {
	jq.mutex.Lock()
	if jq.running {
		jq.mutex.Unlock()
		return
	}
	jq.running = true
	jq.mutex.Unlock()

	// Start the message processing goroutine
	go jq.messageLoop()
}

// messageLoop reads and processes incoming JSON messages
func (jq *JSONQueueProcess) messageLoop() {
	for {
		jq.mutex.Lock()
		running := jq.running
		jq.mutex.Unlock()

		if !running {
			break
		}

		responseJSON, err := jq.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				// The pipe was closed
				break
			}
			log.Printf("Error reading from Python: %v", err)
			continue
		}

		var message map[string]interface{}
		if err := json.Unmarshal(responseJSON, &message); err != nil {
			log.Printf("Error decoding JSON message: %v", err)
			continue
		}

		// Check if this is a response to a request
		if requestID, ok := message["request_id"].(string); ok && !strings.HasPrefix(requestID, "py-") {
			jq.mutex.Lock()
			if ch, exists := jq.responseMap[requestID]; exists {
				ch <- message
				delete(jq.responseMap, requestID)
			}
			jq.mutex.Unlock()
			continue
		}

		// This is a command from Python, process it in a new goroutine
		command, hasCommand := message["command"].(string)
		data := message["data"]
		requestID, hasRequestID := message["request_id"].(string)
		if !hasRequestID {
			fmt.Printf("Warning: Command without request ID: %v\n", message)
		} else {
			if hasCommand {
				jq.processingWg.Add(1)
				go func() {
					defer jq.processingWg.Done()
					jq.processCommand(command, data, requestID)
				}()
			}
		}
	}
}

// processCommand handles a command received from Python
func (jq *JSONQueueProcess) processCommand(command string, data interface{}, requestID string) {
	var response interface{}
	var err error

	// Find and execute the appropriate handler
	jq.mutex.Lock()
	handler, exists := jq.commandHandlers[command]
	defaultHandler := jq.defaultHandler
	jq.mutex.Unlock()

	if exists {
		response, err = handler(data, requestID)
	} else if defaultHandler != nil {
		response, err = defaultHandler(data, requestID)
	} else {
		err = fmt.Errorf("unknown command: %s", command)
	}

	// Send a response if requestID is present
	if requestID != "" {
		responseObj := make(map[string]interface{})

		if err != nil {
			responseObj["error"] = err.Error()
		} else {
			responseObj["result"] = response
		}

		responseObj["request_id"] = requestID

		// Send the response
		jq.mutex.Lock()
		responseJSON, _ := json.Marshal(responseObj)
		_, err = jq.writer.Write(append(responseJSON, '\n'))
		if err == nil {
			err = jq.writer.Flush()
		}
		jq.mutex.Unlock()

		if err != nil {
			log.Printf("Error sending response to Python: %v", err)
		}
	}
}

// generateRequestID generates a unique request ID
func (jq *JSONQueueProcess) generateRequestID() string {
	jq.idMutex.Lock()
	defer jq.idMutex.Unlock()
	id := fmt.Sprintf("req-%d", jq.nextID)
	jq.nextID++
	return id
}

// Read a message from Python
func (jq *JSONQueueProcess) readMessage() (map[string]interface{}, error) {
	line, err := jq.reader.ReadString('\n')
	if err != nil {
		return nil, err
	}

	// Trim any whitespace
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("empty line received")
	}

	// Parse the JSON
	var message map[string]interface{}
	if err := json.Unmarshal([]byte(line), &message); err != nil {
		return nil, fmt.Errorf("error unmarshaling JSON: %v, raw data: %s", err, line)
	}

	return message, nil
}

// Send a message to Python
func (jq *JSONQueueProcess) sendMessage(message map[string]interface{}) error {
	messageJSON, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON message: %w", err)
	}

	jq.mutex.Lock()
	_, err = jq.writer.Write(append(messageJSON, '\n'))
	if err != nil {
		jq.mutex.Unlock()
		return fmt.Errorf("failed to write message: %w", err)
	}

	err = jq.writer.Flush()
	jq.mutex.Unlock()

	if err != nil {
		return fmt.Errorf("failed to flush message: %w", err)
	}

	return nil
}

// SendCommand with error handling
func (jq *JSONQueueProcess) SendCommand(command string, data interface{}, waitForResponse bool) (map[string]interface{}, error) {
	requestID := jq.generateRequestID()
	request := map[string]interface{}{
		"command":    command,
		"data":       data,
		"request_id": requestID,
	}

	// If waiting for response, create a channel to receive it
	var responseChan chan map[string]interface{}
	if waitForResponse {
		responseChan = make(chan map[string]interface{}, 1)
		jq.mutex.Lock()
		jq.responseMap[requestID] = responseChan
		jq.mutex.Unlock()
	}

	// Send the request
	if err := jq.sendMessage(request); err != nil {
		return nil, err
	}

	if !waitForResponse {
		return nil, nil
	}

	// Wait for response with timeout
	select {
	case response := <-responseChan:
		return response, nil
	case <-time.After(30 * time.Second): // 30 second timeout
		jq.mutex.Lock()
		delete(jq.responseMap, requestID)
		jq.mutex.Unlock()
		return nil, fmt.Errorf("timeout waiting for response to command: %s", command)
	}
}

// Close cleans up resources used by the JSONQueueProcess
func (jq *JSONQueueProcess) Close() error {
	// Signal that we're closing
	jq.mutex.Lock()
	if !jq.running {
		jq.mutex.Unlock()
		return nil
	}
	jq.running = false
	jq.mutex.Unlock()

	// Send exit command without waiting for a response
	fmt.Println("Sending exit command to Python process...")
	jq.SendCommand("exit", nil, false)

	// Small delay to allow the command to be sent
	time.Sleep(50 * time.Millisecond)

	// Terminate the process
	return jq.PythonProcess.Terminate()
}

func (jq *JSONQueueProcess) Shutdown() error {
	// Send shutdown command and wait for response
	resp, err := jq.SendCommand("shutdown", nil, true)
	if err != nil {
		return fmt.Errorf("error during shutdown: %w", err)
	}

	fmt.Printf("Shutdown response: %v\n", resp)

	// Wait for Python process to exit
	return jq.PythonProcess.Wait()
}
