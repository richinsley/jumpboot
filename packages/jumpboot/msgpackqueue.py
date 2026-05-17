import asyncio
from jumpboot import msgpack
from jumpboot import BufferPool
import struct
import time
import threading
import sys
import os
import inspect
import time
import traceback
import concurrent.futures
import select
from typing import Any, Dict, Callable, Optional, Union, List, Tuple, IO

def debug_out(msg, file=sys.stderr):
    # print(f"DEBUG MessagePackQueue: {msg}", file=file, flush=True)
    pass

class MessagePackTransport:
    def __init__(self, read_pipe, write_pipe, buffer_size=8192, pool_size=10):
        # Make read pipe non-blocking on Windows
        import os
        if os.name == 'nt':  # Windows
            import msvcrt
            msvcrt.setmode(read_pipe.fileno(), os.O_BINARY)

        self.read_pipe = read_pipe.buffer if hasattr(read_pipe, 'buffer') else read_pipe
        self.write_pipe = write_pipe.buffer if hasattr(write_pipe, 'buffer') else write_pipe

        # Create a buffer pool
        self.buffer_pool = BufferPool(buffer_size, pool_size)

    def send(self, data):
        length = struct.pack(">I", len(data))
        debug_out(f"Sending bytes: {len(data)}", file=sys.stderr)
        debug_out(f"Sending length bytes: {length}", file=sys.stderr)
        self.write_pipe.write(length)
        self.write_pipe.flush()
        self.write_pipe.write(data)
        self.write_pipe.flush()
        debug_out(f"Sent bytes: {len(data)}", file=sys.stderr)
        
    def send_with_timeout(self, data, timeout=5.0):
        """Send data with a timeout.
        
        Args:
            data: The data to send
            timeout: Timeout in seconds
                
        Returns:
            bool: True if successful, False if timed out
        """
        # Skip the complex timeout logic for now and just send the data
        try:
            # The simplest approach - if timeout is provided, we'll at least 
            # check if we exceed it after errors
            start_time = time.time()
            
            self.send(data)
            return True
        except IOError as e:
            # Only handle timeout-related errors
            if timeout is not None and timeout > 0 and time.time() - start_time >= timeout:
                debug_out(f"Send operation timed out after {time.time() - start_time} seconds", file=sys.stderr)
                return False
            # Re-raise other IOErrors
            raise
        
    def receive_with_timeout(self, timeout=5.0):
        """Receive data with a timeout.
        
        Args:
            timeout: Timeout in seconds
                
        Returns:
            bytes: Received data
                
        Raises:
            TimeoutError: If the operation times out
            EOFError: If the pipe is closed
        """
        # Skip the complex timeout logic for now
        try:
            start_time = time.time()
            
            result = self.receive()
            return result
        except IOError as e:
            # Only convert to TimeoutError if we've actually exceeded the timeout
            if timeout is not None and timeout > 0 and time.time() - start_time >= timeout:
                raise TimeoutError("Receive operation timed out")
            # Re-raise other IOErrors
            raise

    def receive(self):
        debug_out("Starting receive operation", file=sys.stderr)
        
        # Read the length prefix
        debug_out("Reading length prefix (4 bytes)", file=sys.stderr)
        length_bytes = self.read_pipe.read(4)
        debug_out(f"Read length bytes: {length_bytes}", file=sys.stderr)
        
        if not length_bytes:
            debug_out("EOF detected - no length bytes received", file=sys.stderr)
            raise EOFError("Pipe closed")
        
        length = struct.unpack(">I", length_bytes)[0]
        debug_out(f"Message length: {length} bytes", file=sys.stderr)
        
        # Get a buffer from the pool if the size is appropriate
        if length <= self.buffer_pool.buffer_size:
            debug_out(f"Using buffer pool for message of size {length}", file=sys.stderr)
            buffer = self.buffer_pool.get()
            
            # Read directly into the buffer
            debug_out("Reading message data into buffer", file=sys.stderr)
            view = memoryview(buffer)[:length]
            bytes_read = 0
            
            while bytes_read < length:
                debug_out(f"Reading chunk at offset {bytes_read}/{length}", file=sys.stderr)
                chunk = self.read_pipe.readinto(view[bytes_read:])
                debug_out(f"Read chunk of size {chunk}", file=sys.stderr)
                
                if not chunk:
                    debug_out("EOF during data read", file=sys.stderr)
                    self.buffer_pool.release(buffer)
                    raise EOFError("Pipe closed during read")
                
                bytes_read += chunk
                debug_out(f"Total bytes read: {bytes_read}/{length}", file=sys.stderr)
            
            # Create a result without copying the data
            debug_out("Creating result from buffer", file=sys.stderr)
            result = bytes(view)
            
            # Return the buffer to the pool
            debug_out("Returning buffer to pool", file=sys.stderr)
            self.buffer_pool.release(buffer)
            
            debug_out(f"Receive complete, returning {len(result)} bytes", file=sys.stderr)
            return result
        else:
            # For larger messages, read in a loop (pipe reads can return partial data)
            debug_out(f"Message too large for buffer pool ({length} bytes), using direct read", file=sys.stderr)
            debug_out("Starting direct read of large message", file=sys.stderr)
            chunks = []
            bytes_read = 0
            while bytes_read < length:
                remaining = length - bytes_read
                debug_out(f"Reading large chunk at offset {bytes_read}/{length}", file=sys.stderr)
                chunk = self.read_pipe.read(remaining)
                if not chunk:
                    debug_out("EOF during large message read", file=sys.stderr)
                    raise EOFError("Pipe closed during read")
                chunks.append(chunk)
                bytes_read += len(chunk)
                debug_out(f"Large read: {bytes_read}/{length} bytes", file=sys.stderr)
            
            data = b"".join(chunks)
            debug_out(f"Large receive complete, returning {len(data)} bytes", file=sys.stderr)
            return data

    def close(self):
        self.read_pipe.close()
        self.write_pipe.close()

class MessagePackQueue:
    def __init__(self, read_pipe, write_pipe):
        self.transport = MessagePackTransport(read_pipe, write_pipe)

    def put(self, obj, block=True, timeout=0):
        try:
            serialized = msgpack.packb(obj)
            if block:
                self._write_with_timeout(serialized, timeout)
            else:
                self._write_non_blocking(serialized)
        except TypeError as e:
            raise ValueError(f"Object of type {type(obj)} is not JSON serializable") from e

    def get(self, block=True, timeout=0):
        if block:
            return self._read_with_timeout(timeout)
        else:
            return self._read_non_blocking()

    def _write_with_timeout(self, data, timeout):
        self.transport.send_with_timeout(data, timeout)

    def _write_non_blocking(self, data):
        self.transport.send(data)

    def _read_with_timeout(self, timeout):
        return msgpack.unpackb(self.transport.receive_with_timeout(timeout))

    def _read_non_blocking(self):
        return msgpack.unpackb(self.transport.receive())

    def close(self):
        self.transport.close()

class MessagePackQueueServer:
    """
    A server that handles MessagePack-based communication with a Go process using the existing MessagePackQueue.
    """
    
    def __init__(self, pipe_in=None, pipe_out=None, auto_start=True, expose_methods=True):
        """
        Initialize the server with customizable pipes.
        
        Args:
            pipe_in: Input pipe (defaults to jumpboot.Pipe_in)
            pipe_out: Output pipe (defaults to jumpboot.Pipe_out)
            auto_start: Whether to automatically start the server
            expose_methods: Whether to automatically expose public methods
        """
        import jumpboot
        
        self.pipe_in = pipe_in if pipe_in is not None else jumpboot.Pipe_in
        self.pipe_out = pipe_out if pipe_out is not None else jumpboot.Pipe_out
        
        # Create a JSONQueue instance for communication
        self.queue = MessagePackQueue(self.pipe_in, self.pipe_out)
        
        # Set up asyncio event loop for non-blocking behavior
        self.loop = asyncio.new_event_loop()
        self.async_thread = None
        
        self.running = False
        self.command_handlers = {}
        self.default_handler = None
        self._response_futures = {}
        self._next_request_id = 0

        # Cancellation registry: maps in-flight request_id -> asyncio.Event.
        # __cancel__ sets the event for a target request_id; cooperative handlers
        # poll it (typically via jb-service's CallContext) to abort early.
        # Phase 1 of the streaming+cancellation design (DESIGN-STREAMING-CANCEL.md
        # in jb-mesh).
        self._inflight_events: Dict[str, asyncio.Event] = {}

        # Streaming registry: maps in-flight request_id -> True if the request
        # came in with the {"stream": true} flag. Higher-level wrappers
        # (jb-service's CallContext) consult this to decide whether
        # ``ctx.emit(chunk)`` publishes a partial frame or is a no-op.
        # Phase 2 of DESIGN-STREAMING-CANCEL.md.
        self._inflight_streaming: Dict[str, bool] = {}

        # For thread safety when accessing shared resources
        self._lock = threading.Lock()

        # Register built-in handlers
        self._register_builtin_handlers()
        
        # Auto-expose methods if requested
        if expose_methods:
            self._expose_methods()
            
        if auto_start:
            self.start()
    
    def _expose_methods(self):
        """
        Automatically expose public methods (those not starting with _) as command handlers.
        """
        for name, method in inspect.getmembers(self, predicate=inspect.ismethod):
            # Skip private methods (starting with _) and already registered handlers
            if name.startswith('_') or name in self.command_handlers:
                continue
                
            # Register the method as a command handler
            self.register_method(name, method)
    
    def register_method(self, name, method):
        """
        Register a class method as a command handler.
        
        Args:
            name: The command name
            method: The method to call
        """
        # Create a wrapper that handles method parameter mapping
        async def method_wrapper(data, request_id):
            # Get the method signature
            sig = inspect.signature(method)
            params = sig.parameters
            
            # Extract method arguments from the data
            kwargs = {}
            
            if data is not None:
                if isinstance(data, dict):
                    # If data is a dict, use it for keyword arguments
                    for param_name in params:
                        if param_name in data:
                            kwargs[param_name] = data[param_name]
                else:
                    # If data is not a dict, pass it as the first argument
                    param_names = list(params.keys())
                    if len(param_names) > 0:
                        kwargs[param_names[0]] = data
            
            # Call the method with the extracted arguments
            result = method(**kwargs)
            
            # If the result is a coroutine, await it
            if inspect.iscoroutine(result):
                result = await result
                
            return result
            
        self.command_handlers[name] = method_wrapper
    
    def _register_builtin_handlers(self):
        """Register built-in command handlers."""
        self.register_handler("exit", self._handle_exit)
        self.register_handler("shutdown", self._handle_shutdown)

        # Add a special handler for method inspection (useful for Go)
        self.register_handler("__get_methods__", self._handle_get_methods)

        # Cooperative cancellation: Go can ask us to cancel an in-flight call.
        self.register_handler("__cancel__", self._handle_cancel)

    def get_cancel_event(self, request_id: str) -> Optional[asyncio.Event]:
        """Return the asyncio.Event associated with an in-flight request_id.

        Higher-level wrappers (e.g. jb-service's CallContext) call this to
        observe whether the caller has cancelled the in-flight call. Returns
        None if no call with that id is registered (already completed, never
        existed, or fire-and-forget).
        """
        return self._inflight_events.get(request_id)

    def is_streaming(self, request_id: str) -> bool:
        """Return True if the in-flight request was sent with stream=True.

        Higher-level wrappers consult this to decide whether ``ctx.emit(chunk)``
        publishes a partial frame or is a no-op. Returns False if the request
        wasn't flagged as streaming, or isn't currently in flight.
        """
        return self._inflight_streaming.get(request_id, False)

    async def _handle_cancel(self, data, request_id):
        """Signal cancellation of a target in-flight request.

        Payload: {"target_request_id": "<id>"}. Sets the asyncio.Event for the
        target request so cooperative handlers can stop early. Returns a small
        ack; callers may ignore it (fire-and-forget is the typical pattern).
        """
        target_id = None
        if isinstance(data, dict):
            target_id = data.get("target_request_id")
        if not target_id:
            return {"ok": False, "error": "missing target_request_id"}
        event = self._inflight_events.get(target_id)
        if event is None:
            # Either already completed or never existed; nothing to cancel.
            return {"ok": False, "error": "no in-flight call", "target_request_id": target_id}
        event.set()
        return {"ok": True, "target_request_id": target_id}
    
    async def _handle_get_methods(self, data, request_id):
        """Return information about exposed methods for Go discovery."""
        methods = {}
        
        for name, method in inspect.getmembers(self, predicate=inspect.ismethod):
            if name.startswith('_') or name not in self.command_handlers:
                continue
                
            # Get signature information
            sig = inspect.signature(method)
            params = []
            
            for param_name, param in sig.parameters.items():
                if param_name == 'self':
                    continue
                    
                param_info = {
                    "name": param_name,
                    "required": param.default is inspect.Parameter.empty
                }
                
                # Add type information if available
                if param.annotation is not inspect.Parameter.empty:
                    param_info["type"] = str(param.annotation)
                    
                params.append(param_info)
            
            # Add return type if available
            return_info = {}
            if sig.return_annotation is not inspect.Parameter.empty:
                return_info["type"] = str(sig.return_annotation)
                
            methods[name] = {
                "parameters": params,
                "return": return_info,
                "doc": inspect.getdoc(method) or ""
            }
            
        return {"methods": methods}
    
    def register_handler(self, command: str, handler: Callable):
        """
        Register a handler function for a specific command.
        
        Args:
            command: The command name
            handler: Function that takes (data, request_id) and returns a response
        """
        # Ensure handler is an async function
        if not inspect.iscoroutinefunction(handler):
            async def async_wrapper(data, request_id):
                return handler(data, request_id)
            self.command_handlers[command] = async_wrapper
        else:
            self.command_handlers[command] = handler
    
    def set_default_handler(self, handler: Callable):
        """Set a handler for commands without a specific handler."""
        if not inspect.iscoroutinefunction(handler):
            async def async_wrapper(command, data, request_id):
                return handler(command, data, request_id)
            self.default_handler = async_wrapper
        else:
            self.default_handler = handler
    
    def start(self):
        """Start the server in a background thread."""
        if self.running:
            return
        
        self.running = True
        self.async_thread = threading.Thread(target=self._run_event_loop)
        self.async_thread.daemon = True
        self.async_thread.start()
    
    def _run_event_loop(self):
        """Run the asyncio event loop in a separate thread."""
        asyncio.set_event_loop(self.loop)
        self.loop.create_task(self._server_loop())
        self.loop.run_forever()
    
    def stop(self):
        """Stop the server."""
        self.running = False
        if self.loop and self.loop.is_running():
            self.loop.call_soon_threadsafe(self.loop.stop)
        if self.async_thread:
            self.async_thread.join(timeout=1.0)
    
    async def _server_loop(self):
        """Main server loop processing incoming JSON messages."""
        try:
            debug_out("Server loop started, waiting for messages...", file=sys.stderr)
            
            # Define this outside the loop to avoid recreating it each time
            def get_message_with_timeout():
                """Get a message with a short timeout to prevent blocking."""
                try:
                    debug_out("Trying to get a message...", file=sys.stderr)
                    message = self.queue.get(block=True, timeout=0.1)
                    debug_out(f"Got message: {message}", file=sys.stderr)
                    return message
                except TimeoutError:
                    return None
                except EOFError:
                    debug_out("EOF detected in queue", file=sys.stderr)
                    self.running = False
                    return None
                except Exception as e:
                    debug_out(f"Error getting message: {e}", file=sys.stderr)
                    traceback.print_exc(file=sys.stderr)
                    return None
            
            while self.running:
                try:
                    # Use a thread for potentially blocking IO
                    with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
                        future = executor.submit(get_message_with_timeout)
                        
                        try:
                            # Wait for the message-getting operation to complete
                            message = await asyncio.wrap_future(future)
                            
                            # If no message, just continue the loop
                            if message is None:
                                await asyncio.sleep(0.01)  # Small sleep to prevent CPU spinning
                                continue
                            
                            debug_out(f"Processing message: {message}", file=sys.stderr)
                            
                            # Extract command, data, request_id, and the streaming flag.
                            # ``stream: true`` (Phase 2) tells the dispatcher to construct
                            # the per-call context with emit() wired to publish partial
                            # frames; without the flag, emit() is a no-op.
                            command = message.get("command")
                            data = message.get("data")
                            request_id = message.get("request_id")
                            stream = bool(message.get("stream"))

                            # Check if this is a response to a pending request
                            if request_id and request_id.startswith("py-"):
                                debug_out(f"This is a response to a Python request: {request_id}", file=sys.stderr)
                                with self._lock:
                                    if request_id in self._response_futures:
                                        future = self._response_futures.pop(request_id)
                                        future.set_result(message)
                                        debug_out(f"Set result for future: {request_id}", file=sys.stderr)
                                continue

                            # Process the command in a separate task
                            asyncio.create_task(self._process_command(command, data, request_id, stream))
                            
                        except Exception as e:
                            debug_out(f"Error processing future: {e}", file=sys.stderr)
                            traceback.print_exc(file=sys.stderr)
                            await asyncio.sleep(0.1)  # Sleep to prevent tight loop on errors
                    
                except asyncio.CancelledError:
                    debug_out("Server loop cancelled", file=sys.stderr)
                    break
                except Exception as e:
                    debug_out(f"Error in server loop: {e}", file=sys.stderr)
                    traceback.print_exc(file=sys.stderr)
                    await asyncio.sleep(0.1)  # Sleep to prevent tight loop on errors
                    
        finally:
            debug_out("MessagePack server stopped", file=sys.stderr)
    
    async def _read_messages(self, queue):
        """Read messages from the input pipe and put them into the queue."""
        # Create a thread-safe reader
        
        # Use a separate thread for reading from the pipe
        def read_from_pipe():
            while self.running:
                try:
                    # Read a message from the pipe (blocking operation)
                    msg_data = self.transport.receive()
                    if not msg_data:
                        # EOF - the pipe was closed
                        debug_out("EOF detected in read thread", file=sys.stderr)
                        self.running = False
                        break
                    
                    # Put the line into a queue that the asyncio loop will process
                    try:
                        # Use call_soon_threadsafe to safely interact with the event loop from another thread
                        self.loop.call_soon_threadsafe(
                            lambda l=msg_data: asyncio.create_task(self._process_message(l, queue))
                        )
                    except Exception as e:
                        debug_out(f"Error scheduling line processing: {e}", file=sys.stderr)
                except Exception as e:
                    debug_out(f"Error reading from pipe: {e}", file=sys.stderr)
                    self.running = False
                    break
        
        # Start the reader thread
        reader_thread = threading.Thread(target=read_from_pipe)
        reader_thread.daemon = True
        reader_thread.start()
        
        # Keep this task alive until the server stops
        while self.running:
            await asyncio.sleep(0.1)
        
        # Wait for reader thread to exit
        reader_thread.join(timeout=1.0)
    
    async def _process_message(self, msg, queue):
        """Process a line read from the pipe."""
        try:
            message = msgpack.unpackb(msg)

            # Put the message into the queue
            await queue.put(message)
            debug_out(f"Put message in queue: {message}", file=sys.stderr)
        except Exception as e:
            debug_out(f"Error processing line: {e}", file=sys.stderr)

    async def _process_command(self, command: str, data: Any, request_id: Optional[str], stream: bool = False):
        """
        Process a command and send a response if needed.

        ``stream`` (Phase 2): when True, the per-call entry is also flagged in
        self._inflight_streaming so higher-level wrappers (jb-service
        CallContext) can decide whether ``emit()`` publishes partial frames.
        """
        debug_out(f"Starting to process command: {command} with request ID: {request_id}", file=sys.stderr)
        # Register a cancel event for this call so __cancel__ can signal it.
        # Skip for the cancel command itself (no point in canceling a cancel) and
        # for fire-and-forget requests with no request_id.
        cancel_event: Optional[asyncio.Event] = None
        if request_id and command != "__cancel__":
            cancel_event = asyncio.Event()
            self._inflight_events[request_id] = cancel_event
            if stream:
                self._inflight_streaming[request_id] = True

        try:
            response = None
            try:
                # Handle the command
                if command in self.command_handlers:
                    debug_out(f"Found handler for command: {command}", file=sys.stderr)
                    response = await self.command_handlers[command](data, request_id)
                    debug_out(f"Handler completed for command: {command}, response: {response}", file=sys.stderr)
                elif self.default_handler:
                    debug_out(f"Using default handler for command: {command}", file=sys.stderr)
                    response = await self.default_handler(command, data, request_id)
                    debug_out(f"Default handler completed for command: {command}", file=sys.stderr)
                else:
                    debug_out(f"No handler found for command: {command}", file=sys.stderr)
                    response = {"error": f"Unknown command: {command}"}

                # Send a response if one was returned and there's a request_id.
                # For streaming requests (Phase 2), the handler may have already
                # published partial frames via send_response({chunk, done:false});
                # the value returned here is the terminal frame and must be
                # wrapped as {"result": <value>, "done": True} so the Go side
                # picks up the user's return value as StreamFrame.Result. The
                # only exception is if the handler already produced a properly
                # shaped terminal frame (has both "result" and "done" keys),
                # in which case we forward it as-is.
                if response is not None and request_id is not None:
                    debug_out(f"Sending response for request ID: {request_id}", file=sys.stderr)
                    if stream:
                        already_wrapped = (
                            isinstance(response, dict)
                            and "done" in response
                            and ("result" in response or "error" in response)
                        )
                        if not already_wrapped:
                            response = {"result": response, "done": True}
                    self.send_response(response, request_id)
                    debug_out(f"Response sent for request ID: {request_id}", file=sys.stderr)

            except Exception as e:
                debug_out(f"Error processing command {command}: {e}", file=sys.stderr)
                traceback.print_exc(file=sys.stderr)
                # Send an error response if there's a request_id. For streaming
                # the error frame must also be marked done:true to close the
                # stream cleanly on the Go side.
                if request_id is not None:
                    error_response = {"error": str(e), "traceback": traceback.format_exc()}
                    if stream:
                        error_response["done"] = True
                    self.send_response(error_response, request_id)
        finally:
            if cancel_event is not None:
                self._inflight_events.pop(request_id, None)
                self._inflight_streaming.pop(request_id, None)
    
    def send_response(self, response: Any, request_id: Optional[str] = None):
        """
        Send a response to the Go process using the queue.
        """
        debug_out(f"Preparing to send response for request ID: {request_id}", file=sys.stderr)
        if request_id is not None:
            if isinstance(response, dict):
                response["request_id"] = request_id
            else:
                response = {"result": response, "request_id": request_id}
        
        # Use the queue.put method to send the response
        try:
            debug_out(f"Sending response: {response}", file=sys.stderr)
            self.queue.put(response)
            debug_out("Response sent successfully", file=sys.stderr)
        except Exception as e:
            debug_out(f"Error sending response: {e}", file=sys.stderr)
            traceback.print_exc(file=sys.stderr)
    
    def _handle_exit(self, data, request_id):
        """Handle the built-in 'exit' command - terminate immediately."""
        debug_out("Received exit command, terminating process...", file=sys.stderr)
        sys.stderr.flush()
        sys.stdout.flush()
        
        # Send a quick response if requested
        if request_id is not None:
            try:
                self.send_response({"status": "exiting"}, request_id)
            except Exception:
                pass  # Ignore errors during exit
        
        # Use os._exit for immediate termination
        os._exit(0)
    
    def _handle_shutdown(self, data, request_id):
        """Handle a graceful shutdown request."""
        debug_out("Received shutdown command, stopping server...", file=sys.stderr)
        
        # Send a response
        if request_id is not None:
            self.send_response({"status": "shutting_down"}, request_id)
        
        # Set running to false to stop the server loop
        self.running = False
        
        # Schedule the event loop to stop
        self.loop.call_soon_threadsafe(self.loop.stop)
        
        return None

    def request(self, command: str, data: Any = None, timeout: float = 5.0) -> Dict:
        """
        Send a command to the Go process and wait for a response.
        """
        # Generate a unique ID for this request
        with self._lock:
            request_id = f"py-{self._next_request_id}"
            self._next_request_id += 1
        
        # Create a future to receive the response
        future = asyncio.Future()
        
        with self._lock:
            self._response_futures[request_id] = future
        
        # Send the request using the queue
        message = {
            "command": command,
            "data": data,
            "request_id": request_id
        }
        
        try:
            debug_out(f"Sending request: {message}", file=sys.stderr)
            self.queue.put(message)
        except Exception as e:
            with self._lock:
                if request_id in self._response_futures:
                    del self._response_futures[request_id]
            raise RuntimeError(f"Error sending request: {e}")
        
        # Wait for the response in the appropriate way
        try:
            return asyncio.run_coroutine_threadsafe(
                self._wait_for_response(future, timeout, request_id),
                self.loop
            ).result(timeout + 0.5)
        except Exception as e:
            with self._lock:
                if request_id in self._response_futures:
                    del self._response_futures[request_id]
            if isinstance(e, TimeoutError) or isinstance(e, asyncio.TimeoutError):
                raise TimeoutError(f"Timeout waiting for response to command '{command}'")
            raise
    
    async def _wait_for_response(self, future, timeout, request_id):
        """Wait for a response future to complete with a timeout."""
        try:
            return await asyncio.wait_for(future, timeout)
        except asyncio.TimeoutError:
            with self._lock:
                if request_id in self._response_futures:
                    del self._response_futures[request_id]
            raise TimeoutError(f"Timeout waiting for response")
    
    # Async version of the request method for use in async methods
    async def async_request(self, command: str, data: Any = None, timeout: float = 5.0) -> Dict:
        """
        Async version of request - send a command to Go and wait for response.
        
        Args:
            command: The command name
            data: The data to send
            timeout: How long to wait for a response (seconds)
            
        Returns:
            The response from the Go process
            
        Raises:
            TimeoutError: If no response is received within the timeout
        """
        # Generate a unique ID for this request
        with self._lock:
            request_id = f"py-{self._next_request_id}"
            self._next_request_id += 1
        
        # Create a future to receive the response
        future = asyncio.Future()
        
        with self._lock:
            self._response_futures[request_id] = future
        
        # Send the request
        message = {
            "command": command,
            "data": data,
            "request_id": request_id
        }
        
        try:
            self.queue.put(message)
        except Exception as e:
            with self._lock:
                if request_id in self._response_futures:
                    del self._response_futures[request_id]
            raise RuntimeError(f"Error sending request: {e}")
        
        # Wait for the response (asynchronously)
        try:
            response = await self._wait_for_response(future, timeout, request_id)
            return response['result']
        except Exception as e:
            with self._lock:
                if request_id in self._response_futures:
                    del self._response_futures[request_id]
            if isinstance(e, asyncio.TimeoutError):
                raise TimeoutError(f"Timeout waiting for response to command '{command}'")
            raise

# Decorator for registering methods in subclasses
def exposed(func):
    """
    Decorator to mark a method as exposed to Go.
    Not required when using auto-expose, but useful for clarity.
    """
    func._exposed = True
    return func


# Helper function to create a server
def create_server(server_class, pipe_in=None, pipe_out=None, auto_start=True):
    """
    Create a JSON server from a given class.
    
    Args:
        server_class: The server class to instantiate
        pipe_in: Input pipe (defaults to jumpboot.Pipe_in)
        pipe_out: Output pipe (defaults to jumpboot.Pipe_out)
        auto_start: Whether to automatically start the server
        
    Returns:
        An instance of the server class
    """
    return server_class(pipe_in=pipe_in, pipe_out=pipe_out, auto_start=auto_start)