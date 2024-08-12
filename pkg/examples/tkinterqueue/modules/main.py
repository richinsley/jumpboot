import jumpboot
import sys

import json
from datetime import datetime, date
import tkinter as tk
from tkinter import ttk
import threading
import queue

json_queue = jumpboot.JSONQueue(sys.Pipe_in, sys.Pipe_out)

class ReaderThread(threading.Thread):
    def __init__(self, json_queue, root, fifo_queue):
        threading.Thread.__init__(self)
        self.json_queue = json_queue
        self.fifo_queue = fifo_queue
        self.root = root
        self.daemon = True
        self.stop_event = threading.Event()

    def run(self):
        while not self.stop_event.is_set():
            try:
                # read a message from the queue
                message = self.json_queue.get()
                # put the message in the FIFO queue
                self.fifo_queue.put(message)
                # Generate a custom event to signal the main thread
                self.root.event_generate("<<NewMessage>>", when="tail")
            except EOFError:
                break
            except Exception as e:
                print(f"Error in reader thread: {e}")
                break

    def stop(self):
        self.stop_event.set()

class TkinterApp:
    def __init__(self, master, json_queue):
        self.master = master
        self.json_queue = json_queue
        
        self.master.title("IPC Tkinter App")
        
        self.text_area = tk.Text(self.master, height=10, width=50)
        self.text_area.pack(pady=10)
        
        self.send_button = ttk.Button(self.master, text="Send Message", command=self.send_message)
        self.send_button.pack(pady=5)
        
        # use a SimpleQueue to communicate with the reader thread
        self.fifo_queue = queue.SimpleQueue()

        self.reader_thread = ReaderThread(self.json_queue, self.master, self.fifo_queue)
        self.reader_thread.start()
        
        # Bind the custom event to a handler
        self.master.bind("<<NewMessage>>", self.on_new_message)
        
        self.master.protocol("WM_DELETE_WINDOW", self.on_closing)

    def send_message(self):
        message = {
            "type": "message",
            "content": "Hello from Python!",
            "timestamp": datetime.now().isoformat()
        }
        self.json_queue.put(message)
        self.text_area.insert(tk.END, f"Sent: {message}\n")

    def on_new_message(self, event):
        message = self.fifo_queue.get()
        self.text_area.insert(tk.END, f"Received: {message}\n")

    def on_closing(self):
        self.reader_thread.stop()
        self.master.destroy()

def main():
    root = tk.Tk()
    app = TkinterApp(root, json_queue)
    root.mainloop()
    
    json_queue.close()

if __name__ == "__main__":
    main()
