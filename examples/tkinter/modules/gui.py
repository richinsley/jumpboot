import tkinter as tk
from tkinter import ttk
import asyncio
import threading
import time
import queue
from jumpboot import JSONQueueServer, exposed

class TkinterGUIService(JSONQueueServer):
    """A service that exposes tkinter GUI controls to Go."""
    
    def __init__(self, pipe_in=None, pipe_out=None):
        # Initialize JSONQueueServer with auto_start=False
        # tkinter MUST be run in the main thread
        super().__init__(pipe_in=pipe_in, pipe_out=pipe_out, auto_start=False)
        
        # Create a command queue for thread-safe UI operations
        self.cmd_queue = queue.Queue()
        
        # Set up the UI in the main thread
        self.setup_ui()
        
        # Now start the server processing in a background thread
        self.start()
        
        # Start processing the tkinter event loop in the main thread
        self.process_ui_events()
    
    def setup_ui(self):
        """Set up the tkinter UI elements."""
        self.root = tk.Tk()
        self.root.title("Go-Python GUI")
        self.root.geometry("600x400")
        
        self.root.protocol("WM_DELETE_WINDOW", self.on_window_close)

        self.label = ttk.Label(self.root, text="Waiting for Go commands...")
        self.label.pack(pady=20)
        
        self.result_var = tk.StringVar(value="")
        self.result_label = ttk.Label(self.root, textvariable=self.result_var)
        self.result_label.pack(pady=10)
        
        self.progress = ttk.Progressbar(self.root, orient=tk.HORIZONTAL, length=300, mode='determinate')
        self.progress.pack(pady=20)
        
        # Store buttons by ID for later reference
        self._buttons = {}
        
        # Configure the root to check the command queue periodically
        self.root.after(100, self.check_command_queue)
    
    def on_window_close(self):
        """Handle window close event."""
        print("Window closing, notifying Go...")
        
        try:
            # Notify Go that the window is closing
            future = asyncio.run_coroutine_threadsafe(
                self.async_request("window_closed", None),
                self.loop
            )
            
            # Wait briefly for Go to acknowledge (but don't block too long)
            try:
                future.result(timeout=0.5)
            except Exception as e:
                print(f"Error notifying Go of window close: {e}")
        except Exception as e:
            print(f"Error sending window close notification: {e}")
        
        # Now destroy the window and stop the server
        self.root.destroy()
        self.stop()
        print("Window closed and server stopped")
        
    def process_ui_events(self):
        """Run the tkinter main loop in the main thread."""
        try:
            self.root.mainloop()
        finally:
            # Ensure we stop the server when the UI closes
            self.stop()
    
    def check_command_queue(self):
        """Check for and process commands in the queue."""
        try:
            # Process all available commands
            while True:
                cmd, args, kwargs, result_event, result_container = self.cmd_queue.get_nowait()
                try:
                    result_container[0] = cmd(*args, **kwargs)
                except Exception as e:
                    result_container[0] = e
                finally:
                    result_event.set()
                self.cmd_queue.task_done()
        except queue.Empty:
            pass
        
        # Schedule the next check if the root window still exists
        if self.root.winfo_exists():
            self.root.after(100, self.check_command_queue)
    
    def _tk_update(self, func, *args, **kwargs):
        """Schedule a function to be executed in the tkinter thread."""
        if not self.root.winfo_exists():
            return False
            
        result_container = [None]  # Use a container to store the result
        event = threading.Event()
        
        # Put the command in the queue
        self.cmd_queue.put((func, args, kwargs, event, result_container))
        
        # Wait for the command to be processed
        event.wait()
        
        # Check if an exception occurred
        if isinstance(result_container[0], Exception):
            raise result_container[0]
            
        return result_container[0]
    
    @exposed
    async def update_label(self, text: str) -> bool:
        """Update the main label text."""
        def _update():
            self.label.config(text=text)
            return True
        
        return self._tk_update(_update)
    
    @exposed
    async def update_result(self, result: str) -> bool:
        """Update the result label text."""
        def _update():
            self.result_var.set(result)
            return True
        
        return self._tk_update(_update)
    
    @exposed
    async def set_progress(self, value: int) -> bool:
        """Set the progress bar value (0-100)."""
        def _update():
            self.progress['value'] = max(0, min(100, value))
            return True
        
        return self._tk_update(_update)
    
    @exposed
    async def create_button(self, text: str, command_name: str) -> str:
        """Create a button that triggers a command back to Go when clicked."""
        button_id = f"button_{id(text)}"
        
        def on_button_click():
            # Use asyncio.run_coroutine_threadsafe to run async code from the tkinter thread
            future = asyncio.run_coroutine_threadsafe(
                self.async_request(command_name, {"button_id": button_id}),
                self.loop
            )
            try:
                # Don't block for too long
                result = future.result(timeout=0.5)
                if result and isinstance(result, str):
                    self.result_var.set(result)
            except Exception as e:
                self.result_var.set(f"Error: {e}")
        
        def _create_button():
            button = ttk.Button(self.root, text=text, command=on_button_click)
            button.pack(pady=10)
            self._buttons[button_id] = button
            return button_id
        
        return self._tk_update(_create_button)
    
    @exposed
    async def quit(self) -> bool:
        """Quit the application."""
        def _quit():
            self.root.quit()
            return True
        
        return self._tk_update(_quit)

# Main entry point
if __name__ == "__main__":
    print("Starting TkinterGUIService...")
    # Create the service - this will start tkinter in the main thread
    service = TkinterGUIService()
    # The service.process_ui_events() method is called in __init__ and will block until the UI is closed