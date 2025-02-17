import json
import jumpboot
import sys
import time
import threading

queue = jumpboot.JSONQueue(jumpboot.Pipe_in, jumpboot.Pipe_out)
cancel_flag = threading.Event()  # Use a threading.Event for cancellation

def handle_command(command, data):
    if command == "greet":
        name = data.get("name", "World")
        return {"message": f"Hello, {name}!"}
    elif command == "calculate":
        x = data.get("x", 0)
        y = data.get("y", 0)
        return {"result": x + y}
    elif command == "long_running":
        cancel_flag.clear() # Reset the flag
        for i in range(10):
            if cancel_flag.is_set():
                print("Long running task cancelled.")
                return {"status": "cancelled"}
            print(f"Long running task: step {i}")
            time.sleep(1)
        return {"status": "completed"}
    elif command == "cancel":
        cancel_flag.set() # Set the cancellation flag
        return None  # No response needed

    else:
        return {"error": "Unknown command"}

def main():
    print("Python process started, waiting for commands...")
    try:
        while True:
            try:
                message = queue.get(block=True, timeout=5)
            except TimeoutError:
                # print("Timeout waiting for message. Checking if parent is still alive...") # Too verbose
                continue
            except EOFError:
                print("Pipe closed, exiting.")
                break
            except Exception as e:
                print(f"Error reading message: {e}", file=sys.stderr)
                break

            if message is None:
                continue

            command = message.get("command")
            data = message.get("data")

            if command == "exit":
                print("Received exit command, exiting.")
                break

            response = handle_command(command, data)
            if response is not None:
                queue.put(response)

    except Exception as e:
        print("Error in main loop:", e)
    finally:
        print("python exiting...")

if __name__ == "__main__":
    main()