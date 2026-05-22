// Package wasmguest is the in-guest SDK for jumpboot WebAssembly plugins.
//
// A jumpboot WASM plugin is an ordinary Go program compiled with
// GOOS=wasip1 GOARCH=wasm. It registers handlers and calls Serve:
//
//	package main
//
//	import "github.com/richinsley/jumpboot/wasmguest"
//
//	func main() {
//		wasmguest.Register("echo", func(data any, ctx *wasmguest.Context) (any, error) {
//			m := data.(map[string]any)
//			return m["text"], nil
//		})
//		wasmguest.Serve()
//	}
//
// Serve speaks the jumpboot queue protocol (docs/PROTOCOL.md) over stdin and
// stdout, so an unmodified Go QueueProcess on the host can drive it. The host
// runs the compiled module in-process with wazero — there is no subprocess.
//
// The package is deliberately stdlib-only (it bundles its own minimal
// MessagePack codec) so a plugin builds with no external dependencies.
//
// Concurrency note: Serve processes one request at a time on a single
// goroutine. Streaming works (a handler may call ctx.Emit before returning),
// but mid-handler cancellation is not observable — ctx.Cancelled always
// reports false. Handlers must not write to os.Stdout: it is the protocol
// channel. Use os.Stderr (or log, which defaults to stderr) for diagnostics.
package wasmguest

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Handler processes a command from the host. data is the decoded argument
// payload (typically a map[string]any); the returned value becomes the
// response. Returning an error sends an error response to the host.
type Handler func(data any, ctx *Context) (any, error)

// Context carries per-call information into a handler.
type Context struct {
	// RequestID is the host's id for this call.
	RequestID string

	stream bool
	out    io.Writer
}

// Stream reports whether the host invoked this call with streaming enabled.
func (c *Context) Stream() bool { return c.stream }

// Cancelled always reports false in this single-goroutine guest — the request
// loop cannot observe a __cancel__ while a handler is running. It exists for
// API parity with the other runtimes.
func (c *Context) Cancelled() bool { return false }

// Emit publishes a partial frame on a streaming call. A map is sent flat with
// done:false; any other value is wrapped as {result, done:false}. Emit is a
// no-op when the call was not invoked with streaming enabled.
func (c *Context) Emit(chunk any) {
	if !c.stream || c.RequestID == "" {
		return
	}
	var partial map[string]any
	if m, ok := chunk.(map[string]any); ok {
		partial = make(map[string]any, len(m)+2)
		for k, v := range m {
			partial[k] = v
		}
	} else {
		partial = map[string]any{"result": chunk}
	}
	partial["done"] = false
	partial["request_id"] = c.RequestID
	_ = writeFrame(c.out, mpEncode(partial))
}

var handlers = map[string]Handler{}

// Register exposes a handler under the given command name. Call it before
// Serve. A later Register for the same name replaces the earlier handler.
func Register(name string, h Handler) {
	handlers[name] = h
}

// Serve runs the queue protocol loop over stdin/stdout until the host closes
// the connection or sends the "exit" command. It does not return until then.
func Serve() {
	for {
		payload, err := readFrame(os.Stdin)
		if err != nil {
			return // EOF or read error — the host has gone away
		}
		decoded, err := mpDecode(payload)
		if err != nil {
			continue // drop a corrupt frame
		}
		msg, ok := decoded.(map[string]any)
		if !ok {
			continue
		}
		dispatch(msg)
	}
}

func dispatch(msg map[string]any) {
	command, _ := msg["command"].(string)
	data := msg["data"]
	requestID, _ := msg["request_id"].(string)
	stream, _ := msg["stream"].(bool)

	switch command {
	case "exit":
		if requestID != "" {
			sendResponse(map[string]any{"status": "exiting"}, requestID)
		}
		os.Exit(0)
	case "shutdown":
		if requestID != "" {
			sendResponse(map[string]any{"status": "shutting_down"}, requestID)
		}
		os.Exit(0)
	case "__get_methods__":
		sendResponse(map[string]any{"methods": describeMethods()}, requestID)
		return
	case "__cancel__":
		// The single-goroutine guest has nothing running concurrently to
		// cancel; acknowledge negatively rather than erroring.
		target, _ := asMap(data)["target_request_id"].(string)
		sendResponse(map[string]any{
			"ok":                false,
			"error":             "no in-flight call",
			"target_request_id": target,
		}, requestID)
		return
	}

	h, ok := handlers[command]
	if !ok {
		sendResponse(map[string]any{"error": "Unknown command: " + command}, requestID)
		return
	}

	ctx := &Context{RequestID: requestID, stream: stream, out: os.Stdout}
	result, err := callHandler(h, data, ctx)
	if requestID == "" {
		return
	}
	if err != nil {
		resp := map[string]any{"error": err.Error()}
		if stream {
			resp["done"] = true
		}
		sendResponse(resp, requestID)
		return
	}
	if stream {
		sendResponse(map[string]any{"result": result, "done": true}, requestID)
	} else {
		sendResponse(result, requestID)
	}
}

// callHandler invokes h, converting a panic into an error so a single bad
// handler cannot take down the whole plugin.
func callHandler(h Handler, data any, ctx *Context) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v", r)
		}
	}()
	return h(data, ctx)
}

func describeMethods() map[string]any {
	methods := make(map[string]any, len(handlers))
	for name := range handlers {
		methods[name] = map[string]any{
			"parameters": []any{},
			"return":     map[string]any{},
			"doc":        "",
		}
	}
	return methods
}

// sendResponse mirrors the other runtimes: a map is sent as-is with request_id
// added; any other value is wrapped as {result, request_id}.
func sendResponse(response any, requestID string) {
	var payload map[string]any
	if m, ok := response.(map[string]any); ok {
		payload = m
	} else {
		payload = map[string]any{"result": response}
	}
	payload["request_id"] = requestID
	_ = writeFrame(os.Stdout, mpEncode(payload))
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// ---- length-prefixed framing (docs/PROTOCOL.md §1) ------------------------

func readFrame(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func writeFrame(w io.Writer, payload []byte) error {
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	_, err := w.Write(frame)
	return err
}
