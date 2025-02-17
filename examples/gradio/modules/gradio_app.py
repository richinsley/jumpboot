import json
import time
import jumpboot
import gradio as gr
import threading
import sys

queue = jumpboot.JSONQueue(jumpboot.Pipe_in, jumpboot.Pipe_out)

def send_command(command, data):
    """Sends a command to Go and waits for the response."""
    request = {"command": command, "data": data, "request_id": str(time.time())}
    # print(f"Sending command: {request}", file=sys.stderr) # Can cause issues if Gradio is waiting
    queue.put(request)
    while True:
        try:
            response = queue.get(block=True, timeout=5)  # Timeout to prevent deadlock
            # print("response:", response) #Debug, can cause issues with Gradio
            if response.get("request_id") == request["request_id"]:
                if "error" in response:
                    print(f"Error from Go: {response['error']}", file=sys.stderr)
                    return None  # Or raise an exception
                else:
                    return response["result"]
        except TimeoutError:
            print("Timeout waiting for response from Go", file=sys.stderr)
            return None #Or raise an exception
        except Exception as e:
            print(f"Error in send_command: {e}", file=sys.stderr)
            return None



# --- Gradio Interface ---

def add_wrapper(x, y):
    return send_command("add", {"x": x, "y": y})

def greet_wrapper(name):
    return send_command("greet", name)

def uppercase_wrapper(text):
    return send_command("uppercase", text)

def reverse_wrapper(numbers):
    return send_command("reverse", numbers)

with gr.Blocks() as demo:
    gr.Markdown("# Go Function Caller")

    with gr.Tab("Add"):
        add_in1 = gr.Number(label="x")
        add_in2 = gr.Number(label="y")
        add_out = gr.Number(label="Result")
        add_btn = gr.Button("Add")
        add_btn.click(add_wrapper, [add_in1, add_in2], add_out)

    with gr.Tab("Greet"):
        greet_in = gr.Textbox(label="Name")
        greet_out = gr.Textbox(label="Greeting")
        greet_btn = gr.Button("Greet")
        greet_btn.click(greet_wrapper, greet_in, greet_out)

    with gr.Tab("Uppercase"):
        uppercase_in = gr.Textbox(label="Text")
        uppercase_out = gr.Textbox(label="Uppercase")
        uppercase_btn = gr.Button("Uppercase")
        uppercase_btn.click(uppercase_wrapper, uppercase_in, uppercase_out)

    with gr.Tab("Reverse"):
        reverse_in = gr.Dataframe(label="Numbers", type="array", datatype="number")
        reverse_out = gr.Dataframe(label="Reversed Numbers", type="array", datatype="number")
        reverse_btn = gr.Button("Reverse")
        reverse_btn.click(reverse_wrapper, reverse_in, reverse_out)

    # Quit button (optional, for clean shutdown)
    quit_btn = gr.Button("Quit")
    def quit_wrapper():
        send_command("exit", None)  # Send exit command.
        time.sleep(0.5)
        return "Exiting..."
    quit_btn.click(quit_wrapper, [], [])

# Enable Gradio's built-in queueing (VERY IMPORTANT)
demo.queue()

# Do NOT run in a separate thread.
if __name__ == "__main__":
    demo.launch(share=False)
    print("Gradio server exited.")