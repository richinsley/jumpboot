package pkg

import (
	"fmt"
	"io"
	"reflect"
	"unsafe"
)

// SharedMemory a cross-platform shared memory object
type SharedMemory struct {
	// shmi is the underlying platform shared memory object
	m   *shmi
	pos int64
}

func (o *SharedMemory) GetSize() int {
	return o.m.getSize()
}

func (o *SharedMemory) GetPtr() unsafe.Pointer {
	return o.m.getPtr()
}

// CreateSharedMemory creates a new shared memory segment
func CreateSharedMemory(name string, size int) (*SharedMemory, error) {
	m, err := create(name, size)
	if err != nil {
		return nil, err
	}
	return &SharedMemory{m, 0}, nil
}

// Open open existing shared memory with the given name
func OpenSharedMemory(name string, size int) (*SharedMemory, error) {
	m, err := open(name, size)
	if err != nil {
		return nil, err
	}
	return &SharedMemory{m, 0}, nil
}

// Close and discard shared memory
func (o *SharedMemory) Close() (err error) {
	if o.m != nil {
		err = o.m.close()
		if err == nil {
			o.m = nil
		}
	}
	return err
}

// Read shared memory (from current position)
func (o *SharedMemory) Read(p []byte) (n int, err error) {
	n, err = o.ReadAt(p, o.pos)
	if err != nil {
		return 0, err
	}
	o.pos += int64(n)
	return n, nil
}

// ReadAt read shared memory (offset)
func (o *SharedMemory) ReadAt(p []byte, off int64) (n int, err error) {
	return o.m.readAt(p, off)
}

// Seek to new read/write position at shared memory
func (o *SharedMemory) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		offset += int64(0)
	case io.SeekCurrent:
		offset += o.pos
	case io.SeekEnd:
		offset += int64(o.m.size)
	}
	if offset < 0 || offset >= int64(o.m.size) {
		return 0, fmt.Errorf("invalid offset")
	}
	o.pos = offset
	return offset, nil
}

// Write byte slice to shared memory (at current position)
func (o *SharedMemory) Write(p []byte) (n int, err error) {
	n, err = o.WriteAt(p, o.pos)
	if err != nil {
		return 0, err
	}
	o.pos += int64(n)
	return n, nil
}

// Write byte slice to shared memory (offset)
func (o *SharedMemory) WriteAt(p []byte, off int64) (n int, err error) {
	return o.m.writeAt(p, off)
}

func GetTypedSlice[T any](shm *SharedMemory, offset int) []T {
	// Calculate the number of elements that can fit in the remaining space
	elementSize := int(unsafe.Sizeof(*new(T)))
	remainingSize := shm.m.size - offset
	numElements := remainingSize / elementSize

	// Create a slice using unsafe.Slice
	return unsafe.Slice((*T)(unsafe.Add(shm.m.v, uintptr(offset))), int(numElements))
}

// Type-specific methods for common types
func (o *SharedMemory) GetFloat32Slice(offset int) []float32 {
	return GetTypedSlice[float32](o, offset)
}

func (o *SharedMemory) GetFloat64Slice(offset int) []float64 {
	return GetTypedSlice[float64](o, offset)
}

func (o *SharedMemory) GetInt32Slice(offset int) []int32 {
	return GetTypedSlice[int32](o, offset)
}

func (o *SharedMemory) GetInt64Slice(offset int) []int64 {
	return GetTypedSlice[int64](o, offset)
}

func (o *SharedMemory) GetByteSlice(offset int) []byte {
	return GetTypedSlice[byte](o, offset)
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
	// Add more types as needed
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

func copySlice2Ptr(b []byte, p uintptr, off int64, size int) int {
	h := reflect.SliceHeader{}
	h.Cap = int(size)
	h.Len = int(size)
	h.Data = p

	bb := *(*[]byte)(unsafe.Pointer(&h))
	return copy(bb[off:], b)
}

func copyPtr2Slice(p uintptr, b []byte, off int64, size int) int {
	h := reflect.SliceHeader{}
	h.Cap = int(size)
	h.Len = int(size)
	h.Data = p

	bb := *(*[]byte)(unsafe.Pointer(&h))
	return copy(b, bb[off:size])
}
