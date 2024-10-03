import jumpboot
import os
import sys
import numpy as np
from multiprocessing import shared_memory
import gc

# we stored the shared memory name and size in the environment and the semaphore name
# in the jumpboot module, so we can access them here
name = jumpboot.SHARED_MEMORY_NAME
size = jumpboot.SHARED_MEMORY_SIZE
semname = jumpboot.SEMAPHORE_NAME

def read_shared_numpy_array(shm):
    buf = shm.buf

    # Read rank
    rank = np.frombuffer(buf[:4], dtype=np.uint32)[0]

    # Read shape
    shape_offset = 4
    shape = np.frombuffer(buf[shape_offset:shape_offset + rank * 4], dtype=np.uint32)

    # Read dtype string
    dtype_offset = shape_offset + rank * 4
    dtype_bytes = buf[dtype_offset:dtype_offset + 16]
    dtype_str = dtype_bytes.tobytes().decode().strip('\x00')

    # Read endianness flag
    endian_offset = dtype_offset + 16
    endian_flag = buf[endian_offset:endian_offset + 1].tobytes()

    # Determine endianness
    if endian_flag == b'L':
        endianness = '<'
    elif endian_flag == b'B':
        endianness = '>'
    else:
        raise ValueError(f"Invalid endianness flag: {endian_flag}")

    # Create dtype with exception handling
    try:
        full_dtype = np.dtype(f"{endianness}{dtype_str}")
    except TypeError:
        full_dtype = np.dtype(dtype_str)

    # Calculate metadata size
    metadata_size = endian_offset + 1

    # Calculate expected data size
    expected_size = np.prod(shape) * full_dtype.itemsize

    # Create NumPy array from shared memory
    arr = np.frombuffer(buf[metadata_size:metadata_size + expected_size], dtype=full_dtype).reshape(shape)
    arr.fill(4)
    
    return arr

# open the named semaphore
sem = jumpboot.NamedSemaphore(semname)

shm = None
np_array = None

try:
    # Attach to the existing shared memory segment
    shm = shared_memory.SharedMemory(name=name, create=False, size=size)
    if shm is not None:
        # Read the NumPy array from shared memory
        np_array = read_shared_numpy_array(shm)
        print(f"Read NumPy array of shape {np_array.shape} and dtype {np_array.dtype}")
        
        # Example operation: calculate mean
        mean_value = np.mean(np_array)
        print(f"Mean value: {mean_value}")
    else:
        print("Failed to open shared memory")
except Exception as e:
    print(f"Error: {e}")
finally:
    # Cleanup
    if np_array is not None:
        np_array = None  # Remove reference to numpy array

    if shm is not None:
        try:
            shm.close()
        except BufferError:
            print("Warning: Unable to close shared memory immediately.")
        finally:
            shm = None  # Remove reference to shared memory object

    # Force garbage collection to release resources
    gc.collect()

    try:
        print("Releasing semaphore")
        sem.release()
    finally:
        sem.close()

print("exit")