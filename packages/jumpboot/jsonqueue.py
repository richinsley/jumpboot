# json queue requirements
import json
import select
import time

class JSONQueue:
    def __init__(self, read_pipe, write_pipe):
        self.read_pipe = read_pipe
        self.write_pipe = write_pipe

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
        start_time = time.time()
        while True:
            _, ready_to_write, _ = select.select([], [self.write_pipe], [], timeout)
            if ready_to_write:
                self.write_pipe.write(data)
                self.write_pipe.flush()
                return
            if timeout is not None and time.time() - start_time >= timeout:
                raise TimeoutError("Write operation timed out")

    def _write_non_blocking(self, data):
        _, ready_to_write, _ = select.select([], [self.write_pipe], [], 0)
        if ready_to_write:
            self.write_pipe.write(data)
            self.write_pipe.flush()
        else:
            raise BlockingIOError("Write would block")

    def _read_with_timeout(self, timeout):
        start_time = time.time()
        buffer = ""
        while True:
            ready_to_read, _, _ = select.select([self.read_pipe], [], [], timeout)
            if ready_to_read:
                chunk = self.read_pipe.readline()
                if not chunk:
                    raise EOFError("Pipe closed")
                buffer += chunk
                if buffer.endswith('\n'):
                    return json.loads(buffer.strip())
            if timeout is not None:
                elapsed = time.time() - start_time
                if elapsed >= timeout:
                    raise TimeoutError("Read operation timed out")
                timeout = max(0, timeout - elapsed)

    def _read_non_blocking(self):
        ready_to_read, _, _ = select.select([self.read_pipe], [], [], 0)
        if ready_to_read:
            data = self.read_pipe.readline()
            if not data:
                raise EOFError("Pipe closed")
            return json.loads(data.strip())
        else:
            raise BlockingIOError("Read would block")

    def close(self):
        self.read_pipe.close()
        self.write_pipe.close()