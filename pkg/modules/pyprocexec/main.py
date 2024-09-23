import jumpboot
import sys
from io import StringIO

def main():
    print("Waiting for message...")
    json_queue = jumpboot.JSONQueue(jumpboot.Pipe_in, jumpboot.Pipe_out)
    while True:
        try:
            # read a message from the queue
            message = json_queue.get()
            if message['type'] == "exit":
                break
            elif message['type'] == "exec":
                # capture the output of the python exec() function
                try:
                    old_stdout = sys.stdout
                    sys.stdout = StringIO()
                    exec(message['code'])
                    output = sys.stdout.getvalue()
                    # create the json response
                    response = {"type": "output", "output": output}
                    json_queue.put(response)
                except Exception as e:
                    output = str(e)
                    # create the json response
                    response = {"type": "error", "output": output}
                    json_queue.put(response)
                finally:
                    sys.stdout = old_stdout
        except EOFError:
            break
        except Exception as e:
            response = {"type": "fatal", "output": str(e)}
            json_queue.put(response)
            break

if __name__ == "__main__":
    main()

