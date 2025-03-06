import asyncio
import json
import time
import threading
import sys
import os
import inspect
import time
import traceback
import concurrent.futures
from typing import Any, Dict, Callable, Optional, Union, List, Tuple, IO

def debug_out(msg, file=sys.stderr):
    # print(f"DEBUG JSONQueue: {msg}", file=file, flush=True)
    pass

class JSONQueue:
    def __init__(self, read_pipe, write_pipe):
        self.read_pipe = read_pipe
        self.write_pipe = write_pipe
        # Make read pipe non-blocking on Windows
        import os
        if os.name == 'nt':  # Windows
            import msvcrt
            msvcrt.setmode(self.read_pipe.fileno(), os.O_BINARY)

    def put(self, obj, block=True, timeout=None):
        try:
            serialized = json.dumps(obj) + '\n'
            if block:
                self._write_with_timeout(serialized, timeout)
            else:
                self._write_non_blocking(serialized)
        except TypeError as e:
            raise ValueError(f"Object of type {type(obj)} is not JSON serializable") from e

    def get(self, block=True, timeout=None):
        if block:
            return self._read_with_timeout(timeout)
        else:
            return self._read_non_blocking()

    def _write_with_timeout(self, data, timeout):
        try:
            self.write_pipe.write(data)
            self.write_pipe.flush()
        except BlockingIOError:
            if timeout is not None:
                raise TimeoutError("Write operation timed out")
            # Keep trying if no timeout specified
            while True:
                try:
                    self.write_pipe.write(data)
                    self.write_pipe.flush()
                    break
                except BlockingIOError:
                    time.sleep(0.001)  # Small sleep to prevent busy waiting

    def _write_non_blocking(self, data):
        try:
            self.write_pipe.write(data)
            self.write_pipe.flush()
        except BlockingIOError:
            raise BlockingIOError("Write would block")

    def _read_with_timeout(self, timeout):
        start_time = time.time()
        buffer = ""
        while True:
            try:
                chunk = self.read_pipe.readline()
                if not chunk:
                    raise EOFError("Pipe closed")
                buffer += chunk
                if buffer.endswith('\n'):
                    return json.loads(buffer.strip())
            except BlockingIOError:
                if timeout is not None:
                    elapsed = time.time() - start_time
                    if elapsed >= timeout:
                        raise TimeoutError("Read operation timed out")
                time.sleep(0.001)  # Small sleep to prevent busy waiting

    def _read_non_blocking(self):
        try:
            data = self.read_pipe.readline()
            if not data:
                raise EOFError("Pipe closed")
            return json.loads(data.strip())
        except BlockingIOError:
            raise BlockingIOError("Read would block")

    def close(self):
        self.read_pipe.close()
        self.write_pipe.close()

class JSONQueueServer:
    """
    A server that handles JSON-based communication with a Go process using the existing JSONQueue.
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
        self.queue = jumpboot.JSONQueue(self.pipe_in, self.pipe_out)
        
        # Set up asyncio event loop for non-blocking behavior
        self.loop = asyncio.new_event_loop()
        self.async_thread = None
        
        self.running = False
        self.command_handlers = {}
        self.default_handler = None
        self._response_futures = {}
        self._next_request_id = 0
        
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
            while self.running:
                try:
                    # Use the existing queue.get method with a timeout
                    message = None
                    
                    # Define a function that will be run in a thread to get messages
                    def get_message():
                        nonlocal message
                        try:
                            message = self.queue.get(block=True, timeout=1.0)
                            return True
                        except TimeoutError:
                            return False
                        except EOFError:
                            debug_out("EOF detected in queue", file=sys.stderr)
                            self.running = False
                            return False
                        except Exception as e:
                            debug_out(f"Error getting message: {e}", file=sys.stderr)
                            return False
                    
                    # Run the get_message function in a thread
                    with concurrent.futures.ThreadPoolExecutor() as executor:
                        future = executor.submit(get_message)
                        try:
                            # Wait for the future to complete with a timeout
                            success = await asyncio.wrap_future(future)
                            if not success or not self.running:
                                continue
                        except Exception as e:
                            debug_out(f"Error waiting for message: {e}", file=sys.stderr)
                            continue
                    
                    # If we got a message, process it
                    if message:
                        debug_out(f"Got message from queue: {message}", file=sys.stderr)
                        
                        command = message.get("command")
                        data = message.get("data")
                        request_id = message.get("request_id")
                        
                        debug_out(f"Processing command: {command} with request ID: {request_id}", file=sys.stderr)
                        
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
                        asyncio.create_task(self._process_command(command, data, request_id))
                    
                except asyncio.CancelledError:
                    debug_out("Server loop cancelled", file=sys.stderr)
                    break
                except Exception as e:
                    debug_out(f"Error in server loop: {e}", file=sys.stderr)
                    traceback.print_exc(file=sys.stderr)
                
        finally:
            debug_out("JSON server stopped", file=sys.stderr)
    
    async def _read_messages(self, queue):
        """Read messages from the input pipe and put them into the queue."""
        # Create a thread-safe reader
        reader = self.pipe_in
        
        # Use a separate thread for reading from the pipe
        def read_from_pipe():
            while self.running:
                try:
                    # Read a line from the pipe (blocking operation)
                    line = reader.readline()
                    if not line:
                        # EOF - the pipe was closed
                        debug_out("EOF detected in read thread", file=sys.stderr)
                        self.running = False
                        break
                    
                    # Put the line into a queue that the asyncio loop will process
                    try:
                        # Use call_soon_threadsafe to safely interact with the event loop from another thread
                        self.loop.call_soon_threadsafe(
                            lambda l=line: asyncio.create_task(self._process_line(l, queue))
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
    
    async def _process_line(self, line, queue):
        """Process a line read from the pipe."""
        try:
            # Parse the JSON message
            line = line.strip()
            if not line:
                return
                
            message = json.loads(line)
            
            # Put the message into the queue
            await queue.put(message)
            debug_out(f"Put message in queue: {message}", file=sys.stderr)
        except json.JSONDecodeError as e:
            debug_out(f"Error decoding JSON message: {line} - {e}", file=sys.stderr)
        except Exception as e:
            debug_out(f"Error processing line: {e}", file=sys.stderr)

    async def _process_command(self, command: str, data: Any, request_id: Optional[str]):
        """
        Process a command and send a response if needed.
        """
        debug_out(f"Starting to process command: {command} with request ID: {request_id}", file=sys.stderr)
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
            
            # Send a response if one was returned and there's a request_id
            if response is not None and request_id is not None:
                debug_out(f"Sending response for request ID: {request_id}", file=sys.stderr)
                self.send_response(response, request_id)
                debug_out(f"Response sent for request ID: {request_id}", file=sys.stderr)
            
        except Exception as e:
            debug_out(f"Error processing command {command}: {e}", file=sys.stderr)
            traceback.print_exc(file=sys.stderr)
            # Send an error response if there's a request_id
            if request_id is not None:
                error_response = {"error": str(e), "traceback": traceback.format_exc()}
                self.send_response(error_response, request_id)
    
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
                self.pipe_out.flush()  # Ensure the response is sent
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
            message_json = json.dumps(message) + "\n"
            self.pipe_out.write(message_json)
            self.pipe_out.flush()
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

# class JSONQueueServer:
#     """
#     A server that handles JSON-based communication with a Go process.
    
#     Methods can be automatically exposed to Go, allowing for direct method calls
#     from Go to Python class methods.
#     """
    
#     def __init__(self, pipe_in=None, pipe_out=None, auto_start=True, expose_methods=True):
#         """
#         Initialize the server with customizable pipes.
        
#         Args:
#             pipe_in: Input pipe (defaults to jumpboot.Pipe_in)
#             pipe_out: Output pipe (defaults to jumpboot.Pipe_out)
#             auto_start: Whether to automatically start the server thread
#             expose_methods: Whether to automatically expose public methods
#         """
#         import jumpboot
        
#         self.pipe_in = pipe_in if pipe_in is not None else jumpboot.Pipe_in
#         self.pipe_out = pipe_out if pipe_out is not None else jumpboot.Pipe_out
        
#         self.queue = jumpboot.JSONQueue(self.pipe_in, self.pipe_out)
#         self.running = False
#         self.server_thread = None
#         self.command_handlers = {}
#         self.default_handler = None
#         self._response_channels = {}
        
#         # Register built-in handlers
#         self._register_builtin_handlers()
        
#         # Auto-expose methods if requested
#         if expose_methods:
#             self._expose_methods()
            
#         if auto_start:
#             self.start()
    
#     def _expose_methods(self):
#         """
#         Automatically expose public methods (those not starting with _) as command handlers.
#         """
#         for name, method in inspect.getmembers(self, predicate=inspect.ismethod):
#             # Skip private methods (starting with _) and already registered handlers
#             if name.startswith('_') or name in self.command_handlers:
#                 continue
                
#             # Register the method as a command handler
#             self.register_method(name, method)
    
#     def register_method(self, name, method):
#         """
#         Register a class method as a command handler.
        
#         Args:
#             name: The command name
#             method: The method to call
#         """
#         # Create a wrapper that handles method parameter mapping
#         def method_wrapper(data, request_id):
#             # Get the method signature
#             sig = inspect.signature(method)
#             params = sig.parameters
            
#             # Extract method arguments from the data
#             kwargs = {}
            
#             if data is not None:
#                 if isinstance(data, dict):
#                     # If data is a dict, use it for keyword arguments
#                     for param_name in params:
#                         if param_name in data:
#                             kwargs[param_name] = data[param_name]
#                 else:
#                     # If data is not a dict, pass it as the first argument
#                     param_names = list(params.keys())
#                     if len(param_names) > 0:
#                         kwargs[param_names[0]] = data
            
#             # Call the method with the extracted arguments
#             result = method(**kwargs)
#             return result
            
#         self.command_handlers[name] = method_wrapper
    
#     def _register_builtin_handlers(self):
#         """Register built-in command handlers."""
#         self.register_handler("exit", self._handle_exit)
        
#         # Add a special handler for method inspection (useful for Go)
#         self.register_handler("__get_methods__", self._handle_get_methods)
    
#     def _handle_get_methods(self, data, request_id):
#         """Return information about exposed methods for Go discovery."""
#         methods = {}
        
#         for name, method in inspect.getmembers(self, predicate=inspect.ismethod):
#             if name.startswith('_') or name not in self.command_handlers:
#                 continue
                
#             # Get signature information
#             sig = inspect.signature(method)
#             params = []
            
#             for param_name, param in sig.parameters.items():
#                 if param_name == 'self':
#                     continue
                    
#                 param_info = {
#                     "name": param_name,
#                     "required": param.default is inspect.Parameter.empty
#                 }
                
#                 # Add type information if available
#                 if param.annotation is not inspect.Parameter.empty:
#                     param_info["type"] = str(param.annotation)
                    
#                 params.append(param_info)
            
#             # Add return type if available
#             return_info = {}
#             if sig.return_annotation is not inspect.Parameter.empty:
#                 return_info["type"] = str(sig.return_annotation)
                
#             methods[name] = {
#                 "parameters": params,
#                 "return": return_info,
#                 "doc": inspect.getdoc(method) or ""
#             }
            
#         return {"methods": methods}
    
#     def register_handler(self, command: str, handler: Callable):
#         """
#         Register a handler function for a specific command.
        
#         Args:
#             command: The command name
#             handler: Function that takes (data, request_id) and returns a response
#         """
#         self.command_handlers[command] = handler
    
#     def set_default_handler(self, handler: Callable):
#         """Set a handler for commands without a specific handler."""
#         self.default_handler = handler
    
#     def start(self):
#         """Start the server in a background thread."""
#         if self.running:
#             return
        
#         self.running = True
#         self.server_thread = threading.Thread(target=self._server_loop)
#         self.server_thread.daemon = True
#         self.server_thread.start()
    
#     def stop(self):
#         """Stop the server."""
#         self.running = False
#         if self.server_thread:
#             self.server_thread.join(timeout=1.0)
    
#     def _server_loop(self):
#         """Main server loop processing incoming JSON messages."""
#         try:
#             while self.running:
#                 try:
#                     message = self.queue.get(block=True, timeout=1.0)
#                     if not message:
#                         continue
                    
#                     command = message.get("command")
#                     data = message.get("data")
#                     request_id = message.get("request_id")
                    
#                     # Check if this is a response to a pending request
#                     if request_id and request_id.startswith("py-") and request_id in self._response_channels:
#                         channel = self._response_channels.pop(request_id)
#                         channel.append(message)
#                         continue
                    
#                     # Process the command
#                     self._process_command(command, data, request_id)
                    
#                 except TimeoutError:
#                     continue
#                 except EOFError:
#                     debug_out("Pipe closed, exiting server loop", file=sys.stderr)
#                     self.running = False
#                     break
#                 except Exception as e:
#                     debug_out(f"Error in server loop: {e}", file=sys.stderr)
#                     traceback.print_exc(file=sys.stderr)
#         finally:
#             debug_out("JSON server stopped", file=sys.stderr)
    
#     def _process_command(self, command: str, data: Any, request_id: Optional[str]):
#         """
#         Process a command and send a response if needed.
        
#         Args:
#             command: The command to process
#             data: The data associated with the command
#             request_id: Optional ID to include in the response
#         """
#         response = None
#         try:
#             # Handle the command
#             if command in self.command_handlers:
#                 response = self.command_handlers[command](data, request_id)
#             elif self.default_handler:
#                 response = self.default_handler(command, data, request_id)
#             else:
#                 response = {"error": f"Unknown command: {command}"}
            
#             # Send a response if one was returned and there's a request_id
#             if response is not None and request_id is not None:
#                 self.send_response(response, request_id)
                
#         except Exception as e:
#             # Send an error response if there's a request_id
#             if request_id is not None:
#                 error_response = {"error": str(e), "traceback": traceback.format_exc()}
#                 self.send_response(error_response, request_id)
    
#     def send_response(self, response: Any, request_id: Optional[str] = None):
#         """
#         Send a response to the Go process.
        
#         Args:
#             response: The response data
#             request_id: Optional ID to include in the response
#         """
#         if request_id is not None:
#             if isinstance(response, dict):
#                 response["request_id"] = request_id
#             else:
#                 response = {"result": response, "request_id": request_id}
        
#         self.queue.put(response)
    
#     def _handle_exit(self, data, request_id):
#         """Handle the built-in 'exit' command."""
#         debug_out("Received exit command, stopping server...", file=sys.stderr)
#         self.running = False
#         return {"status": "exiting"}
    
#     def send_command(self, command: str, data: Any = None) -> None:
#         """
#         Send a command to the Go process without expecting a response.
        
#         Args:
#             command: The command name
#             data: The data to send
#         """
#         message = {
#             "command": command,
#             "data": data
#         }
#         self.queue.put(message)
    
#     def request(self, command: str, data: Any = None, timeout: float = 5.0) -> Dict:
#         """
#         Send a command to the Go process and wait for a response.
        
#         This is useful if the Python side needs to request something from Go.
        
#         Args:
#             command: The command name
#             data: The data to send
#             timeout: How long to wait for a response (seconds)
            
#         Returns:
#             The response from the Go process
        
#         Raises:
#             TimeoutError: If no response is received within the timeout
#         """
#         # Generate a unique ID for this request
#         request_id = f"py-{time.time()}-{id(data)}"
        
#         # Create a response channel
#         response_channel = []
#         self._response_channels[request_id] = response_channel
        
#         # Send the request
#         message = {
#             "command": command,
#             "data": data,
#             "request_id": request_id
#         }
#         self.queue.put(message)
        
#         # Wait for the response
#         start_time = time.time()
#         while time.time() - start_time < timeout:
#             if response_channel:
#                 return response_channel[0]
#             time.sleep(0.01)
            
#         # Clean up and raise timeout
#         if request_id in self._response_channels:
#             del self._response_channels[request_id]
            
#         raise TimeoutError(f"Timeout waiting for response to command '{command}'")


# # Decorator for registering methods in subclasses
# def exposed(func):
#     """
#     Decorator to mark a method as exposed to Go.
#     Not required when using auto-expose, but useful for clarity.
#     """
#     func._exposed = True
#     return func


# # Helper function to create a server
# def create_server(server_class, pipe_in=None, pipe_out=None, auto_start=True):
#     """
#     Create a JSON server from a given class.
    
#     Args:
#         server_class: The server class to instantiate
#         pipe_in: Input pipe (defaults to jumpboot.Pipe_in)
#         pipe_out: Output pipe (defaults to jumpboot.Pipe_out)
#         auto_start: Whether to automatically start the server
        
#     Returns:
#         An instance of the server class
#     """
#     return server_class(pipe_in=pipe_in, pipe_out=pipe_out, auto_start=auto_start)