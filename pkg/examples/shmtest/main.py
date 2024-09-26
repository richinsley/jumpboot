import jumpboot
import os
import sys
import numpy as np
from multiprocessing import shared_memory

# we stored the shared memory name and size in the environment and the semaphore name
# in the jumpboot module, so we can access them here
name = jumpboot.SHARED_MEMORY_NAME
size = jumpboot.SHARED_MEMORY_SIZE
semname = jumpboot.SEMAPHORE_NAME

def read_shared_numpy_array(shm):
    # Read metadata
    buf = shm.buf
    rank = np.frombuffer(buf[:4], dtype=np.uint32)[0]
    shape = np.frombuffer(buf[4:4+rank*4], dtype=np.uint32)
    dtype_str = buf[4+rank*4:20+rank*4].tobytes().decode().strip('\x00')
    endian_flag = buf[20+rank*4:21+rank*4].tobytes()

    # print(f"dtype_str: {dtype_str}")
    # print(f"endian_flag: {endian_flag}")
    # print(f"shape: {shape}")

    # Determine endianness
    if endian_flag == b'L':
        endianness = '<'
    elif endian_flag == b'B':
        endianness = '>'
    else:
        raise ValueError(f"Invalid endianness flag: {endian_flag}")

    # Create dtype
    try:
        full_dtype = np.dtype(f"{endianness}{dtype_str}")
    except TypeError:
        full_dtype = np.dtype(dtype_str)

    print(f"Full dtype: {full_dtype}")

    # Calculate data offset
    metadata_size = 21 + rank * 4  # 4 for rank, 4*rank for shape, 16 for dtype, 1 for endianness

    # Calculate expected data size
    expected_size = np.prod(shape) * full_dtype.itemsize

    # Ensure the buffer is the correct size
    if len(buf) < metadata_size + expected_size:
        raise ValueError(f"Buffer size ({len(buf)}) is smaller than expected ({metadata_size + expected_size})")

    # Create NumPy array from shared memory
    arr = np.frombuffer(buf[metadata_size:metadata_size+expected_size], dtype=full_dtype).reshape(shape)

    # print(f"Array shape: {arr.shape}")
    # print(f"Array dtype: {arr.dtype}")

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

    # Force garbage collection
    # import gc
    # gc.collect()

    try:
        print("Releasing semaphore")
        sem.release()
    finally:
        sem.close()

print("exit")