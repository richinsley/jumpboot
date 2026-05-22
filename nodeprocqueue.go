package jumpboot

import "os"

// NewQueueProcess starts a Node.js process with bidirectional RPC communication
// and returns a QueueProcess driving it. It is the Node.js counterpart of
// PythonEnvironment.NewQueueProcess and reuses the language-neutral
// newQueueProcess core from pyprocqueue.go — all of the RPC machinery
// (Call, handlers, streaming, cancellation) is shared.
//
// Parameters:
//   - program: the NodeProgram to execute (its main module should construct a
//     MessagePackQueueServer from the embedded jumpboot SDK)
//   - serviceStruct: optional Go struct whose exported methods become command
//     handlers Node can invoke
//   - environment_vars: additional environment variables for the process
//   - extrafiles: additional file handles to pass to Node
//
// The returned QueueProcess has a nil PythonProcess field; use the promoted
// RuntimeProcess methods (Alive, ExitChan, Terminate, ...) for lifecycle access.
func (env *NodeEnvironment) NewQueueProcess(program *NodeProgram, serviceStruct interface{}, environment_vars map[string]string, extrafiles []*os.File) (*QueueProcess, error) {
	nodeProcess, _, err := env.NewNodeProcessFromProgram(program, environment_vars, extrafiles)
	if err != nil {
		return nil, err
	}
	return newQueueProcess(nodeProcess, serviceStruct)
}
