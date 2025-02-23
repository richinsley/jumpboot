//go:build darwin && cgo
// +build darwin,cgo

package jumpboot

/*
#include <sys/mman.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <stdio.h>
#include <unistd.h>
#include <sys/errno.h>

int _create(const char* name, int size, int flag) {
    // First try to remove any existing segment in that name
    shm_unlink(name);  // It's okay if this fails

    mode_t mode = S_IRUSR | S_IWUSR | S_IRGRP | S_IWGRP;

    int fd = shm_open(name, flag | O_CREAT, mode);
    if (fd < 0) {
        return -1;
    }

    // Single ftruncate call is sufficient
    if (ftruncate(fd, size) != 0) {
        int err = errno;  // Save errno for debugging
        close(fd);
        shm_unlink(name);
        return -2;
    }

    return fd;
}

int Create(const char* name, int size) {
	int flag = O_RDWR | O_CREAT;
	return _create(name, size, flag);
}

int Open(const char* name, int size) {
	int flag = O_RDWR;
	return _create(name, size, flag);
}

void* Map(int fd, int size) {
	void* p = mmap(
		NULL, size,
		PROT_READ | PROT_WRITE,
		MAP_SHARED, fd, 0);
	if (p == MAP_FAILED) {
		return NULL;
	}
	return p;
}

void Close(int fd, void* p, int size) {
	if (p != NULL) {
		munmap(p, size);
	}
	if (fd != 0) {
		close(fd);
	}
}

void Delete(const char* name) {
	shm_unlink(name);
}
*/
import "C"

import (
	"fmt"
	"io"
	"unsafe"
)

type shmi struct {
	name   string
	fd     C.int
	v      unsafe.Pointer
	size   int
	parent bool
}

func (o *shmi) getSize() int {
	return o.size
}

func (o *shmi) getPtr() unsafe.Pointer {
	return o.v
}

// create shared memory. return shmi object.
// name should not be more than 31 bytes.
func create(name string, size int) (*shmi, error) {
	name = "/" + name

	fd := C.Create(C.CString(name), C.int(size))
	if fd < 0 {
		return nil, fmt.Errorf("create")
	}

	v := C.Map(fd, C.int(size))
	if v == nil {
		C.Close(fd, nil, C.int(size))
		C.Delete(C.CString(name))
	}

	return &shmi{name, fd, v, size, true}, nil
}

// open shared memory. return shmi object.
// name should not be more than 31 bytes.
func open(name string, size int) (*shmi, error) {
	name = "/" + name

	fd := C.Open(C.CString(name), C.int(size))
	if fd < 0 {
		return nil, fmt.Errorf("open")
	}

	v := C.Map(fd, C.int(size))
	if v == nil {
		C.Close(fd, nil, C.int(size))
		C.Delete(C.CString(name))
	}

	return &shmi{name, fd, v, size, false}, nil
}

func (o *shmi) close() error {
	if o.v != nil {
		C.Close(o.fd, o.v, C.int(o.size))
		o.v = nil
	}
	if o.parent {
		C.Delete(C.CString(o.name))
	}
	return nil
}

// read shared memory. return read size.
func (o *shmi) readAt(p []byte, off int64) (n int, err error) {
	if off >= int64(o.size) {
		return 0, io.EOF
	}
	if max := int64(o.size) - off; int64(len(p)) > max {
		p = p[:max]
	}
	return copyPtr2Slice(uintptr(o.v), p, off, o.size), nil
}

// write shared memory. return write size.
func (o *shmi) writeAt(p []byte, off int64) (n int, err error) {
	if off >= int64(o.size) {
		return 0, io.EOF
	}
	if max := int64(o.size) - off; int64(len(p)) > max {
		p = p[:max]
	}
	return copySlice2Ptr(p, uintptr(o.v), off, o.size), nil
}
