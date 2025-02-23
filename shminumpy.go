package jumpboot

import (
	"encoding/binary"
	"fmt"
	"unsafe"
)

func CreateSharedNumPyArray[T any](name string, shape []int) (*SharedMemory, int, error) {
	// Calculate total size
	size := 1
	for _, dim := range shape {
		size *= dim
	}

	// Add extra space for metadata (shape, dtype, and endianness flag)
	metadataSize := 4 + len(shape)*4 + 16 + 1 // 4 bytes for rank, 4 bytes per dimension, 16 bytes for dtype, 1 byte for endianness
	totalSize := metadataSize + size*int(unsafe.Sizeof(new(T)))

	// Create shared memory
	shm, err := CreateSharedMemory(name, totalSize)
	if err != nil {
		return nil, 0, err
	}

	if shm.GetPtr() == nil && totalSize > 0 {
		shm.Close() // Close before returning the error
		return nil, 0, fmt.Errorf("shared memory mapping failed (nil pointer)")
	}

	// Get the byte slice for metadata
	metadataSlice := unsafe.Slice((*byte)(shm.GetPtr()), metadataSize)

	// Write metadata
	binary.LittleEndian.PutUint32(metadataSlice[:4], uint32(len(shape)))
	for i, dim := range shape {
		binary.LittleEndian.PutUint32(metadataSlice[4+i*4:8+i*4], uint32(dim))
	}
	dtype := GetDType[T]()
	copy(metadataSlice[4+len(shape)*4:20+len(shape)*4], []byte(dtype))

	// Write endianness flag
	metadataSlice[20+len(shape)*4] = 'L' // 'L' for little-endian

	return shm, totalSize, nil
}

// Helper function to get the data type string
func GetDType[T any]() string {
	switch any(new(T)).(type) {
	case *float32:
		return "float32"
	case *float64:
		return "float64"
	case *int32:
		return "int32"
	case *int64:
		return "int64"
	case *uint32:
		return "uint32"
	case *uint64:
		return "uint64"
	case *complex64:
		return "complex64"
	case *complex128:
		return "complex128"
	case *bool:
		return "bool"
	case *int8:
		return "int8"
	case *uint8:
		return "uint8"
	case *int16:
		return "int16"
	case *uint16:
		return "uint16"
	// TODO - Add more types
	default:
		return "unknown"
	}
}

// GetDTypeSize returns the size in bytes of a given data type
func GetDTypeSize(dtype string) int {
	switch dtype {
	case "float32":
		return 4
	case "float64":
		return 8
	case "int32":
		return 4
	case "int64":
		return 8
	case "uint32":
		return 4
	case "uint64":
		return 8
	case "complex64":
		return 8
	case "complex128":
		return 16
	case "bool":
		return 1
	case "int8", "uint8", "byte":
		return 1
	case "int16", "uint16":
		return 2
	default:
		panic(fmt.Sprintf("Unsupported dtype: %s", dtype))
	}
}
