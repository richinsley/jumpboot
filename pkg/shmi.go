package pkg

import (
	"fmt"
	"io"
	"reflect"
	"unsafe"
)

// Memory is shared memory struct
type Memory struct {
	// shmi is the underlying platform shared memory object
	m   *shmi
	pos int64
}

// CreateSharedMemory creates a new shared memory segment
func CreateSharedMemory(name string, size int) (*Memory, error) {
	m, err := create(name, size)
	if err != nil {
		return nil, err
	}
	return &Memory{m, 0}, nil
}

// Open open existing shared memory with the given name
func OpenSharedMemory(name string, size int) (*Memory, error) {
	m, err := open(name, size)
	if err != nil {
		return nil, err
	}
	return &Memory{m, 0}, nil
}

// Close and discard shared memory
func (o *Memory) Close() (err error) {
	if o.m != nil {
		err = o.m.close()
		if err == nil {
			o.m = nil
		}
	}
	return err
}

// Read shared memory (from current position)
func (o *Memory) Read(p []byte) (n int, err error) {
	n, err = o.ReadAt(p, o.pos)
	if err != nil {
		return 0, err
	}
	o.pos += int64(n)
	return n, nil
}

// ReadAt read shared memory (offset)
func (o *Memory) ReadAt(p []byte, off int64) (n int, err error) {
	return o.m.readAt(p, off)
}

// Seek to new read/write position at shared memory
func (o *Memory) Seek(offset int64, whence int) (int64, error) {
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
func (o *Memory) Write(p []byte) (n int, err error) {
	n, err = o.WriteAt(p, o.pos)
	if err != nil {
		return 0, err
	}
	o.pos += int64(n)
	return n, nil
}

// Write byte slice to shared memory (offset)
func (o *Memory) WriteAt(p []byte, off int64) (n int, err error) {
	return o.m.writeAt(p, off)
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
