//go:build !windows
// +build !windows

package jumpboot

/*
#include <stdlib.h>
#include <stdio.h>
#include <errno.h>
#include <fcntl.h>
#include <semaphore.h>
#include <time.h>

sem_t* create_semaphore(const char* name, int value) {
    #ifdef __APPLE__
    sem_t* sem = sem_open(name, O_CREAT, 0644, value);
	if (sem == SEM_FAILED) {
		return NULL;
	}
    #else
    sem_t* sem = sem_open(name, O_CREAT, 0644, value);
    if (sem == SEM_FAILED) {
        return NULL;
    }
    #endif
    return sem;
}

sem_t* open_semaphore(const char* name) {
    return sem_open(name, 0);
}

int close_semaphore(sem_t* sem) {
    return sem_close(sem);
}

int remove_semaphore(const char* name) {
    return sem_unlink(name);
}

int wait_semaphore(sem_t* sem) {
    return sem_wait(sem);
}

int try_wait_semaphore(sem_t* sem) {
    return sem_trywait(sem);
}

int timed_wait_semaphore(sem_t* sem, long long timeout_ns) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    ts.tv_sec += timeout_ns / 1000000000LL;
    ts.tv_nsec += timeout_ns % 1000000000LL;
    if (ts.tv_nsec >= 1000000000) {
        ts.tv_sec++;
        ts.tv_nsec -= 1000000000;
    }
    #ifdef __APPLE__
    while (1) {
        if (sem_trywait(sem) == 0) {
            return 0;
        }
        if (errno != EAGAIN) {
            return -1;
        }
        struct timespec current_time;
        clock_gettime(CLOCK_REALTIME, &current_time);
        if (current_time.tv_sec > ts.tv_sec || (current_time.tv_sec == ts.tv_sec && current_time.tv_nsec >= ts.tv_nsec)) {
            errno = ETIMEDOUT;
            return -1;
        }
        nanosleep(&(struct timespec){.tv_nsec=100000}, NULL); // Sleep for 100 microseconds
    }
    #else
    return sem_timedwait(sem, &ts);
    #endif
}

int post_semaphore(sem_t* sem) {
    return sem_post(sem);
}

// get errno
int get_errno() {
	return errno;
}
*/
import "C"
import (
	"fmt"
	"time"
	"unsafe"
)

type posixSemaphore struct {
	sem  *C.sem_t
	name string
}

func NewSemaphore(name string, initialValue int) (Semaphore, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	sem := C.create_semaphore(cName, C.int(initialValue))
	if sem == nil {
		return nil, fmt.Errorf("failed to create semaphore: %s", name)
	}

	return &posixSemaphore{sem: sem, name: name}, nil
}

func OpenSemaphore(name string) (Semaphore, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	sem := C.open_semaphore(cName)
	if sem == nil {
		return nil, fmt.Errorf("failed to open semaphore: %s", name)
	}

	return &posixSemaphore{sem: sem, name: name}, nil
}

func (s *posixSemaphore) Acquire() error {
	if C.wait_semaphore(s.sem) != 0 {
		return fmt.Errorf("failed to acquire semaphore")
	}
	return nil
}

func (s *posixSemaphore) Release() error {
	if C.post_semaphore(s.sem) != 0 {
		return fmt.Errorf("failed to release semaphore")
	}
	return nil
}

func (s *posixSemaphore) TryAcquire() (bool, error) {
	res := C.try_wait_semaphore(s.sem)
	if res == 0 {
		return true, nil
	}
	if C.get_errno() != C.EAGAIN {
		return false, fmt.Errorf("error trying to acquire semaphore")
	}
	return false, nil
}

func (s *posixSemaphore) AcquireTimeout(timeoutMs int) (bool, error) {
	timeoutNs := C.longlong(time.Duration(timeoutMs) * time.Millisecond / time.Nanosecond)
	res := C.timed_wait_semaphore(s.sem, timeoutNs)
	if res == 0 {
		return true, nil
	}
	if C.get_errno() == C.ETIMEDOUT {
		return false, nil
	}
	return false, fmt.Errorf("error acquiring semaphore with timeout")
}

func (s *posixSemaphore) Close() error {
	if C.close_semaphore(s.sem) != 0 {
		return fmt.Errorf("failed to close semaphore")
	}
	return nil
}

func RemoveSemaphore(name string) error {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	if C.remove_semaphore(cName) != 0 {
		return fmt.Errorf("failed to remove semaphore: %s", name)
	}
	return nil
}
