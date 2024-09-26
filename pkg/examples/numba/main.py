import jumpboot
import numpy as np
from multiprocessing import shared_memory
from numba import cuda
import math  # We'll use math.exp instead of np.exp
import gc

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

    return arr

@cuda.jit
def cuda_operation(input_array, output_array):
    """
    This CUDA kernel computes the exponential of each element
    and adds its index values.
    """
    i, j = cuda.grid(2)
    if i < input_array.shape[0] and j < input_array.shape[1]:
        output_array[i, j] = math.exp(input_array[i, j]) + i + j

def process_array_with_cuda(input_array):
    # Explicitly transfer the input array to the device
    input_device_array = cuda.to_device(input_array)
    output_device_array = cuda.device_array_like(input_array)

    # Set up the grid and block dimensions
    threads_per_block = (16, 16)
    blocks_per_grid_x = (input_array.shape[0] + threads_per_block[0] - 1) // threads_per_block[0]
    blocks_per_grid_y = (input_array.shape[1] + threads_per_block[1] - 1) // threads_per_block[1]
    blocks_per_grid = (blocks_per_grid_x, blocks_per_grid_y)

    # Launch the CUDA kernel with device arrays
    cuda_operation[blocks_per_grid, threads_per_block](input_device_array, output_device_array)

    # Synchronize to ensure the kernel has finished executing
    cuda.synchronize()

    # Copy the result back to the host
    result_array = output_device_array.copy_to_host()

    return result_array

# Open the named semaphore
sem = jumpboot.NamedSemaphore(semname)

try:
    # Attach to the existing shared memory segment
    shm = shared_memory.SharedMemory(name=name, create=False, size=size)
    if shm is not None:
        # Read the NumPy array from shared memory
        np_array = read_shared_numpy_array(shm)
        print(f"Read NumPy array of shape {np_array.shape} and dtype {np_array.dtype}")
        
        # Process the array with CUDA
        result = process_array_with_cuda(np_array)

        print("CUDA processing complete")
        print(f"Result shape: {result.shape}")
        print(f"Sample output values: {result[0, 0]}, {result[-1, -1]}")

        # Example: calculate mean of the result
        mean_value = np.mean(result)
        print(f"Mean value of result: {mean_value}")

        # Delete all references to np_array
        del np_array

        # Run garbage collector to ensure all memoryviews are freed
        gc.collect()
    else:
        print("Failed to open shared memory")
except Exception as e:
    print(f"Error: {e}")
finally:
    if 'shm' in locals():
        try:
            shm.close()
            shm.unlink()  # Attempt to unlink the shared memory
        except Exception as e:
            print(f"Warning: Error during shared memory cleanup: {e}")
        finally:
            del shm  # Explicitly delete the shared memory object

    try:
        print("Releasing semaphore")
        sem.release()
    finally:
        sem.close()

print("exit")