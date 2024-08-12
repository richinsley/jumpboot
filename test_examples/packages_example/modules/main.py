# main.py
import sys
from math_operations import add, subtract, multiply, divide
from tabulate import tabulate
import debugpy
import os
import jumpboot

print("Starting main.py")
print(f"__name__: {__name__}")
print(f"__file__: {__file__}")
print(f"__package__: {__package__}")
print(jumpboot.Pipe_In)

def main():

    # project_root = os.path.abspath(os.path.dirname(__file__))
    # print(f"Project root: {project_root}")
    # debugpy.listen(("localhost", 5678))
    # print("Waiting for debugger attach...")
    # debugpy.wait_for_client()
    # print("Debugger attached")
    # debugpy.breakpoint()
    
    a = 10
    b = 5

    print(f"{a} + {b} = {add(a, b)}")
    print(f"{a} - {b} = {subtract(a, b)}")
    print(f"{a} * {b} = {multiply(a, b)}")
    print(f"{a} / {b} = {divide(a, b)}")

    table = [["Sun",696000,1989100000],["Earth",6371,5973.6], ["Moon",1737,73.5],["Mars",3390,641.85]]
    print(tabulate(table))

    # write end message to a queue
    queue = jumpboot.JSONQueue(sys.Pipe_in, sys.Pipe_out)
    queue.put({"message": "end"})

    # read the response from the queue
    response = queue.get()
    print(response)

    # # write "end" to Pipe_Out
    # sys.Pipe_out.write("end\n")
    # sys.Pipe_out.flush()

print("Defining main() complete")

if __name__ == "__main__":
    print("__main__ block entered")
    main()
else:
    print(f"__name__ is {__name__}, not running main()")

print("End of main.py")