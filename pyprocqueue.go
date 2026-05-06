package jumpboot

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"
)

// QueueProcess provides bidirectional RPC-style communication between Go and Python.
// It uses MessagePack serialization over pipes for efficient message passing.
//
// QueueProcess is safe for concurrent use by multiple goroutines. Each Call() is
// serialized via an internal mutex, and responses are correlated with requests using
// unique IDs. Command handlers registered via RegisterHandler are invoked in separate
// goroutines and may execute concurrently.
//
// QueueProcess supports:
//   - Calling Python methods from Go with Call() or the fluent On().Do().Call() API
//   - Registering Go handlers that Python can invoke
//   - Automatic method discovery from Python
//   - Request/response correlation with unique IDs
//   - Timeout support for long-running operations
//
// Example:
//
//	queue, _ := env.NewQueueProcess(program, nil, nil, nil)
//	result, _ := queue.Call("process_data", 30, map[string]interface{}{"input": data})
//	queue.Close()
type QueueProcess struct {
	*PythonProcess

	// serializer handles message encoding/decoding (MessagePack)
	serializer Serializer

	// transport handles the wire protocol (length-prefixed binary)
	transport Transport

	// mutex protects concurrent access to shared state
	mutex sync.Mutex

	// responseMap tracks pending requests awaiting responses
	responseMap map[string]chan map[string]interface{}

	// commandHandlers maps command names to Go handler functions
	commandHandlers map[string]CommandHandler

	// defaultHandler is invoked for commands without a specific handler
	defaultHandler CommandHandler

	// nextID is the counter for generating unique request IDs
	nextID int64

	// idMutex protects nextID
	idMutex sync.Mutex

	// methodCache stores discovered Python method metadata
	methodCache map[string]MethodInfo

	// running indicates whether the message loop is active
	running bool

	// processingWg tracks in-flight command handlers
	processingWg sync.WaitGroup
}

// CommandHandler is a function that handles commands received from Python.
// It receives the command data and request ID, and returns a response or error.
// Handlers are registered with RegisterHandler and invoked by the message loop.
type CommandHandler func(data interface{}, requestID string) (interface{}, error)

// methodCall represents a fluent builder for calling Python methods.
// Use QueueProcess.On() to create a methodCall, then chain Do() and Call().
type methodCall struct {
	process       *QueueProcess
	methodName    string
	data          map[string]interface{}
	timeout       time.Duration
	reflectResult bool
}

// RegisterHandler registers a Go function to handle a specific command from Python.
// When Python sends a command with this name, the handler is invoked with the data.
// The handler's return value is sent back to Python as the response.
func (jq *QueueProcess) RegisterHandler(command string, handler CommandHandler) {
	jq.mutex.Lock()
	defer jq.mutex.Unlock()
	jq.commandHandlers[command] = handler
}

// SetDefaultHandler sets a fallback handler for commands without a specific handler.
// If no handler is registered for a command and no default is set, an error is returned.
func (jq *QueueProcess) SetDefaultHandler(handler CommandHandler) {
	jq.mutex.Lock()
	defer jq.mutex.Unlock()
	jq.defaultHandler = handler
}

// MethodInfo contains metadata about an exposed Python method,
// discovered via the __get_methods__ introspection command.
type MethodInfo struct {
	// Parameters describes the method's parameters.
	Parameters []ParameterInfo `json:"parameters"`

	// Return contains return type information (if available).
	Return map[string]string `json:"return"`

	// Doc is the Python docstring for the method.
	Doc string `json:"doc"`
}

// ParameterInfo describes a single parameter of a Python method.
type ParameterInfo struct {
	// Name is the parameter name.
	Name string `json:"name"`

	// Required indicates if the parameter has no default value.
	Required bool `json:"required"`

	// Type is the type annotation (if available).
	Type string `json:"type,omitempty"`
}

// NewQueueProcess creates a Python process with bidirectional RPC communication.
//
// Parameters:
//   - program: The PythonProgram to execute (should implement the queue protocol)
//   - serviceStruct: Optional Go struct whose exported methods become command handlers.
//     Reflection is used to register each method automatically.
//   - environment_vars: Additional environment variables for the process
//   - extrafiles: Additional file handles to pass to Python
//
// The function starts the message loop automatically and discovers Python methods
// via introspection. Python stdout/stderr are forwarded to Go's os.Stdout/os.Stderr.
func (env *PythonEnvironment) NewQueueProcess(program *PythonProgram, serviceStruct interface{}, environment_vars map[string]string, extrafiles []*os.File) (*QueueProcess, error) {
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

	jq := &QueueProcess{
		PythonProcess: pyProcess,
		serializer:    MsgpackSerializer{},
		transport:     NewMsgpackTransport(pyProcess.PipeIn, pyProcess.PipeOut),
		responseMap:   make(map[string]chan map[string]interface{}),
		nextID:        1,
		methodCache:   make(map[string]MethodInfo),
		commandHandlers: map[string]CommandHandler{},
	}

	// Start process monitoring — detects crashes and unblocks pending calls
	pyProcess.OnExit = func(exitErr error) {
		jq.handleProcessExit(exitErr)
	}
	pyProcess.MonitorProcess()

	if serviceStruct != nil {
		// --- Reflect over the serviceStruct ---
		serviceValue := reflect.ValueOf(serviceStruct)
		serviceType := serviceValue.Type()

		// Iterate over the methods of the struct
		for i := 0; i < serviceType.NumMethod(); i++ {
			method := serviceType.Method(i)

			// Check if the method is exported (starts with uppercase)
			if method.PkgPath != "" { // PkgPath is empty for exported methods
				continue
			}

			// Create a CommandHandler function that uses reflection to call the method
			handler := func(data interface{}, requestID string) (interface{}, error) {
				// 1. Convert data to []reflect.Value (handling nil)
				var args []reflect.Value
				args = append(args, serviceValue) // Add the receiver first

				if data != nil {
					// Expect data to be an array/slice
					if dataArray, ok := data.([]interface{}); ok {
						// Check if the number of arguments matches the method signature
						if len(dataArray) != method.Type.NumIn()-1 { // -1 to exclude the receiver
							return nil, fmt.Errorf("incorrect number of arguments for method %s", method.Name)
						}

						// Process each argument
						for i, arg := range dataArray {
							paramType := method.Type.In(i + 1) // +1 to skip the receiver
							argValue := reflect.ValueOf(arg)

							// Check if the argument can be converted to the parameter type
							if !argValue.CanConvert(paramType) {
								return nil, fmt.Errorf("cannot convert argument %d to type %s for method %s", i, paramType, method.Name)
							}

							// Convert the argument to the correct type
							convertedValue := argValue.Convert(paramType)
							args = append(args, convertedValue)
						}
					} else {
						return nil, fmt.Errorf("invalid data format for method %s, expected array", method.Name)
					}
				}

				// 2. Call the method using reflection
				results := method.Func.Call(args)

				// 3. Check for errors (assume the last result is an error)
				if len(results) > 0 {
					if err, ok := results[len(results)-1].Interface().(error); ok && err != nil {
						return nil, fmt.Errorf("error calling method %s: %w", method.Name, err)
					}
				}

				// 4. Return the result (or nil if no result)
				if len(results) > 1 {
					return results[0].Interface(), nil
				}
				return nil, nil
			}

			// Register the handler
			jq.RegisterHandler(method.Name, handler)
		}
	}

	// Start the message processing.
	// Start() is the single launch point for messageLoop; starting a second
	// reader on the same pipe can race/corrupt MessagePack frames and strand
	// RPC callers waiting for responses that were consumed by the wrong reader.
	jq.Start()

	// Fetch method info from Python
	err = jq.discoverMethods()
	if err != nil {
		// Not fatal, just log it
		fmt.Printf("Warning: Failed to discover Python methods: %v\n", err)
	}

	return jq, nil
}

// discoverMethods fetches information about exposed Python methods
func (jq *QueueProcess) discoverMethods() error {
	response, err := jq.SendCommand("__get_methods__", nil, 0, true)
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

// Call invokes a Python method by name and returns the result.
//
// Parameters:
//   - methodName: The Python method to call
//   - timeoutSeconds: Maximum seconds to wait (0 for unlimited)
//   - args: Arguments to pass (typically a map or slice)
//
// Returns the result from Python, or an error if the call failed or timed out.
func (jq *QueueProcess) Call(methodName string, timeoutSeconds int, args interface{}) (interface{}, error) {
	response, err := jq.SendCommand(methodName, args, timeoutSeconds, true)
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

// GetMethods returns the names of all discovered Python methods.
// Methods are discovered during NewQueueProcess via introspection.
func (jq *QueueProcess) GetMethods() []string {
	var methods []string
	for name := range jq.methodCache {
		methods = append(methods, name)
	}
	return methods
}

// GetMethodInfo returns metadata about a specific Python method.
// Returns the MethodInfo and true if found, or an empty MethodInfo and false if not.
func (jq *QueueProcess) GetMethodInfo(methodName string) (MethodInfo, bool) {
	info, ok := jq.methodCache[methodName]
	return info, ok
}

// Start begins the message processing loop.
// This is called automatically by NewQueueProcess; manual calls are idempotent.
func (jq *QueueProcess) Start() {
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

// messageLoop continuously reads messages from Python and dispatches them.
// Responses to Go requests are routed via responseMap; commands from Python
// are handled by registered handlers in separate goroutines.
func (jq *QueueProcess) messageLoop() {
	for {
		jq.mutex.Lock()
		running := jq.running
		jq.mutex.Unlock()

		if !running {
			break
		}

		response, err := jq.transport.Receive()
		if err != nil {
			if err == io.EOF {
				// The pipe was closed
				break
			}
			log.Printf("Error reading from Python: %v", err)
			continue
		}

		var message map[string]interface{}
		// if err := json.Unmarshal(response, &message); err != nil {
		// 	log.Printf("Error decoding JSON message: %v", err)
		// 	continue
		// }
		if err := jq.serializer.Unmarshal(response, &message); err != nil {
			log.Printf("Error decoding message: %v", err)
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

// processCommand dispatches a command from Python to the appropriate handler.
// If a handler is registered for the command, it's invoked; otherwise the default
// handler is used. The response is sent back to Python with the matching requestID.
func (jq *QueueProcess) processCommand(command string, data interface{}, requestID string) {
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
		// responseJSON, _ := json.Marshal(responseObj)
		response, _ := jq.serializer.Marshal(responseObj)
		err = jq.transport.Send(response)
		if err == nil {
			// err = jq.writer.Flush()
			err = jq.transport.Flush()
		}
		jq.mutex.Unlock()

		if err != nil {
			log.Printf("Error sending response to Python: %v", err)
		}
	}
}

// generateRequestID generates a unique request ID
func (jq *QueueProcess) generateRequestID() string {
	jq.idMutex.Lock()
	defer jq.idMutex.Unlock()
	id := fmt.Sprintf("req-%d", jq.nextID)
	jq.nextID++
	return id
}

// Read a message from Python
func (jq *QueueProcess) readMessage() (map[string]interface{}, error) {
	//line, err := jq.reader.ReadString('\n')
	line, err := jq.transport.Receive()
	if err != nil {
		return nil, err
	}

	// // Trim any whitespace
	// line = strings.TrimSpace(line)
	// if line == "" {
	// 	return nil, fmt.Errorf("empty line received")
	// }

	// Parse the JSON
	var message map[string]interface{}
	if err := json.Unmarshal([]byte(line), &message); err != nil {
		return nil, fmt.Errorf("error unmarshaling JSON: %v, raw data: %s", err, line)
	}

	return message, nil
}

// Send a message to Python
func (jq *QueueProcess) sendMessage(message map[string]interface{}) error {
	msgdata, err := jq.serializer.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	jq.mutex.Lock()
	err = jq.transport.Send(msgdata)
	if err != nil {
		jq.mutex.Unlock()
		return fmt.Errorf("failed to write message: %w", err)
	}

	// err = jq.writer.Flush()
	err = jq.transport.Flush()
	jq.mutex.Unlock()

	if err != nil {
		return fmt.Errorf("failed to flush message: %w", err)
	}

	return nil
}

// SendCommand sends a command to Python with optional response waiting.
//
// Parameters:
//   - command: The command name (method name in Python)
//   - data: The command arguments
//   - timeoutSeconds: Maximum seconds to wait (0 for unlimited, ignored if not waiting)
//   - waitForResponse: If true, blocks until Python responds
//
// Returns the response map (if waiting) or nil, and any error encountered.
func (jq *QueueProcess) SendCommand(command string, data interface{}, timeoutSeconds int, waitForResponse bool) (map[string]interface{}, error) {
	// Fail fast if process is dead
	if !jq.PythonProcess.Alive() {
		return nil, fmt.Errorf("%w: %v", ErrProcessExited, jq.PythonProcess.ExitError())
	}

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
		if waitForResponse {
			jq.mutex.Lock()
			delete(jq.responseMap, requestID)
			jq.mutex.Unlock()
		}
		return nil, err
	}

	if !waitForResponse {
		return nil, nil
	}

	// Wait for response, with process exit as an escape hatch
	exitChan := jq.PythonProcess.ExitChan()

	if timeoutSeconds <= 0 {
		select {
		case response := <-responseChan:
			return response, nil
		case <-exitChan:
			// Process died while waiting — check if handleProcessExit drained our channel
			select {
			case response := <-responseChan:
				return response, nil
			default:
				return nil, fmt.Errorf("%w: %v", ErrProcessExited, jq.PythonProcess.ExitError())
			}
		}
	} else {
		select {
		case response := <-responseChan:
			return response, nil
		case <-exitChan:
			select {
			case response := <-responseChan:
				return response, nil
			default:
				return nil, fmt.Errorf("%w: %v", ErrProcessExited, jq.PythonProcess.ExitError())
			}
		case <-time.After(time.Duration(timeoutSeconds) * time.Second):
			jq.mutex.Lock()
			delete(jq.responseMap, requestID)
			jq.mutex.Unlock()
			return nil, fmt.Errorf("timeout waiting for response to command: %s", command)
		}
	}
}

// handleProcessExit is called when the Python process exits unexpectedly.
// It marks the QueueProcess as stopped and drains all pending response channels
// so that callers blocked in SendCommand unblock with an error.
func (jq *QueueProcess) handleProcessExit(exitErr error) {
	jq.mutex.Lock()
	jq.running = false

	// Drain all pending response channels so blocked callers unblock
	for id, ch := range jq.responseMap {
		ch <- map[string]interface{}{
			"error":      fmt.Sprintf("python process exited: %v", exitErr),
			"request_id": id,
		}
		delete(jq.responseMap, id)
	}
	jq.mutex.Unlock()
}

// Close stops the message loop and terminates the Python process.
// It sends an "exit" command to Python (without waiting for response) and
// then forcefully terminates the process after a brief delay.
func (jq *QueueProcess) Close() error {
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
	jq.SendCommand("exit", nil, 0, false)

	// Small delay to allow the command to be sent
	time.Sleep(50 * time.Millisecond)

	// Terminate the process
	return jq.PythonProcess.Terminate()
}

// Shutdown gracefully stops the QueueProcess by sending a "shutdown" command
// and waiting for Python to exit cleanly. Use this instead of Close when you
// need to ensure Python completes any cleanup operations.
func (jq *QueueProcess) Shutdown() error {
	// Send shutdown command and wait for response
	resp, err := jq.SendCommand("shutdown", nil, 0, true)
	if err != nil {
		return fmt.Errorf("error during shutdown: %w", err)
	}

	fmt.Printf("Shutdown response: %v\n", resp)

	// Wait for Python process to exit
	return jq.PythonProcess.Wait()
}

// On begins a fluent method call chain for the specified Python method.
// Use Do() to add arguments and Call() to execute:
//
//	result, err := queue.On("process").Do("input", data, "verbose", true).Call()
func (jq *QueueProcess) On(methodName string) *methodCall {
	return &methodCall{
		process:    jq,
		methodName: methodName,
		timeout:    0, // Default timeout (wait indefinitely)
		data:       make(map[string]interface{}),
	}
}

// Do adds named arguments to the method call as key-value pairs.
// Arguments must be provided as alternating string keys and values:
//
//	.Do("name", "Alice", "age", 30, "active", true)
//
// Panics if the number of arguments is odd or if keys are not strings.
func (mc *methodCall) Do(args ...interface{}) *methodCall {
	if len(args)%2 != 0 {
		panic("invalid number of arguments: must be even")
	}

	for i := 0; i < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			panic("invalid argument type: even arguments must be strings")
		}
		mc.data[key] = args[i+1]
	}

	return mc
}

// WithTimeout sets the maximum duration to wait for the method to complete.
// A zero timeout means wait indefinitely.
func (mc *methodCall) WithTimeout(timeout time.Duration) *methodCall {
	mc.timeout = timeout
	return mc
}

// Call executes the Python method and returns the result.
// The method is called with arguments added via Do() and the timeout set via WithTimeout().
func (mc *methodCall) Call() (interface{}, error) {
	// ... (Add validation for parameter names and types here, potentially using GetMethodInfo)

	if mc.timeout > 0 {
		// Use SendCommand with timeout
		response, err := mc.process.SendCommand(mc.methodName, mc.data, int(mc.timeout.Seconds()), true)
		return extractResult(response, err)
	}

	// Use SendCommand without timeout
	response, err := mc.process.SendCommand(mc.methodName, mc.data, 0, true)
	return extractResult(response, err)
}

// CallReflect executes the Python method and unmarshals the result into target.
// Target must be a non-nil pointer. For complex types, JSON marshaling/unmarshaling
// is used to handle nested structures.
//
// Example:
//
//	var users []User
//	err := queue.On("get_users").CallReflect(&users)
func (mc *methodCall) CallReflect(target interface{}) error {
	// Get the raw result
	result, err := mc.Call()
	if err != nil {
		return err
	}

	targetValue := reflect.ValueOf(target)
	if targetValue.Kind() != reflect.Ptr || targetValue.IsNil() {
		return fmt.Errorf("target must be a non-nil pointer")
	}

	targetElem := targetValue.Elem()

	// Special handling for slices
	if targetElem.Kind() == reflect.Slice {
		resultSlice, ok := result.([]interface{})
		if !ok {
			return fmt.Errorf("expected slice but got %T", result)
		}

		// Convert the result to JSON and back to properly handle nested structures
		jsonData, err := json.Marshal(resultSlice)
		if err != nil {
			return fmt.Errorf("failed to marshal result: %v", err)
		}

		return json.Unmarshal(jsonData, target)
	}

	// Handle non-slice types
	resultValue := reflect.ValueOf(result)
	if !resultValue.Type().AssignableTo(targetElem.Type()) {
		// Try JSON conversion for complex types
		jsonData, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("failed to marshal result: %v", err)
		}

		return json.Unmarshal(jsonData, target)
	}

	targetElem.Set(resultValue)
	return nil
}

// extractResult extracts the result value from a Python response, handling errors.
func extractResult(response map[string]interface{}, err error) (interface{}, error) {
	if err != nil {
		return nil, fmt.Errorf("error calling Python method: %w", err)
	}

	if errMsg, ok := response["error"].(string); ok {
		return nil, fmt.Errorf("python error: %s", errMsg)
	}

	return response["result"], nil
}
