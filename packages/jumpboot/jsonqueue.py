import json
import time

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