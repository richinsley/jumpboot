package jumpboot

// NewQueueProcess compiles the WASM plugin, instantiates it in-process with
// wazero, and returns a QueueProcess driving it. It is the WebAssembly
// counterpart of PythonEnvironment.NewQueueProcess / NodeEnvironment.
// NewQueueProcess and reuses the language-neutral newQueueProcess core — the
// RPC machinery (Call, handlers, streaming) is shared unchanged.
//
// There are no environment-variable or extra-file parameters: a WASM plugin
// runs in-process and is sandboxed by wazero, with no OS process or file
// descriptors to configure.
//
// The returned QueueProcess has a nil PythonProcess field; use the promoted
// RuntimeProcess methods (Alive, ExitChan, Terminate, ...) for lifecycle access.
func (env *WasmEnvironment) NewQueueProcess(program *WasmProgram, serviceStruct interface{}) (*QueueProcess, error) {
	wasmProcess, err := env.NewWasmProcessFromProgram(program)
	if err != nil {
		return nil, err
	}
	return newQueueProcess(wasmProcess, serviceStruct)
}
