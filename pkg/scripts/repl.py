import os
import sys
import traceback
import code
from contextlib import redirect_stdout, redirect_stderr
import contextlib
import argparse
import io

DELIMITER = "\x01\x02\x03\n"  # Custom delimiter with non-visible ASCII characters

class REPLInterpreter(code.InteractiveConsole):

    def __init__(self, locals=None):
        super().__init__(locals=locals)
        self.__CAPTURE_COMBINED__ = True   # Flag to capture both stdout and stderr
    
    def conrun(self, source, filename="<input>", symbol="single"):
        try:
            # Use StringIO for capturing stdout and stderr
            stdout_f = io.StringIO() if self.__CAPTURE_COMBINED__ else None
            stderr_f = io.StringIO() if self.__CAPTURE_COMBINED__ else None
            result = False
            
            if self.__CAPTURE_COMBINED__:
                with redirect_stdout(stdout_f), redirect_stderr(stderr_f):
                    result = self.push(source)
                    if result:
                        result = self.push('')
            else:
                result = self.push(source)
                if result:
                    result = self.push('')

            # Write the captured stdout to the output_pipe
            if self.__CAPTURE_COMBINED__:
                global_output_pipe.write(stdout_f.getvalue())
                global_output_pipe.flush()

            # Write the captured stderr to the output_pipe
            if self.__CAPTURE_COMBINED__:
                global_output_pipe.write(stderr_f.getvalue())
                global_output_pipe.flush()

            return result
        except Exception:
            global_output_pipe.write(f"Error: {traceback.format_exc()}{DELIMITER}")
            global_output_pipe.flush()
            return False

def run_repl(input_pipe, output_pipe):
    global global_output_pipe
    global_output_pipe = output_pipe
    
    # Initialize the REPL interpreter with stdout and stderr redirection options
    repl = REPLInterpreter()
    code_buffer = ""  # Buffer for multiline code input
    gotdelim = False

    while True:
        try:
            # Read from the input pipe until we get the delimiter
            linecount = 0
            
            while True:
                line = input_pipe.readline()
                linecount += 1
                if line.endswith(DELIMITER):
                    code_buffer += line[:-len(DELIMITER)]
                    gotdelim = True
                    break
                code_buffer += line

            # if code_buffer starts with "__CAPTURE_COMBINED__ =" then update the flag instead of appending to code_buffer
            if code_buffer.startswith("__CAPTURE_COMBINED__ =") and linecount == 1:
                repl.__CAPTURE_COMBINED__ = eval(code_buffer.split("=")[1].strip())
                # Reset the code buffer and line count
                code_buffer = ""
                linecount = 0
                gotdelim = False
                continue

            # Feed the complete code block to the interpreter
            more = repl.conrun(code_buffer)

            # Once the block is complete, clear buffer after execution
            code_buffer = ""  # Reset buffer for next input block
            gotdelim = False
            linecount = 0
            global_output_pipe.write(DELIMITER)
            global_output_pipe.flush()

        except Exception as e:
            # Write the error traceback to the output pipe
            global_output_pipe.write(f"Error: {traceback.format_exc()}{DELIMITER}")
            global_output_pipe.flush()

if __name__ == "__main__":
    run_repl(sys.Pipe_in, sys.Pipe_out)
