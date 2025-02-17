//go:build windows
// +build windows

package jumpboot

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32DLL             *windows.LazyDLL
	procCreateSemaphore     *windows.LazyProc
	procOpenSemaphore       *windows.LazyProc
	procReleaseSemaphore    *windows.LazyProc
	procWaitForSingleObject *windows.LazyProc
	procCloseHandle         *windows.LazyProc
	initOnce                sync.Once
)

func initProcs() {
	kernel32DLL = windows.NewLazySystemDLL("kernel32.dll")
	procCreateSemaphore = kernel32DLL.NewProc("CreateSemaphoreW")
	procOpenSemaphore = kernel32DLL.NewProc("OpenSemaphoreW")
	procReleaseSemaphore = kernel32DLL.NewProc("ReleaseSemaphore")
	procWaitForSingleObject = kernel32DLL.NewProc("WaitForSingleObject")
	procCloseHandle = kernel32DLL.NewProc("CloseHandle")
}

type windowsSemaphore struct {
	handle windows.Handle
	name   string
}

func NewSemaphore(name string, initialValue int) (Semaphore, error) {
	initOnce.Do(initProcs)

	utf16Name, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("failed to convert semaphore name: %w", err)
	}

	handle, _, err := procCreateSemaphore.Call(
		0,
		uintptr(initialValue),
		0x7fffffff,
		uintptr(unsafe.Pointer(utf16Name)),
	)

	if handle == 0 {
		return nil, fmt.Errorf("failed to create semaphore: %w", err)
	}

	return &windowsSemaphore{handle: windows.Handle(handle), name: name}, nil
}

func OpenSemaphore(name string) (Semaphore, error) {
	initOnce.Do(initProcs)

	utf16Name, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("failed to convert semaphore name: %w", err)
	}

	handle, _, err := procOpenSemaphore.Call(
		windows.SEMAPHORE_ALL_ACCESS,
		0,
		uintptr(unsafe.Pointer(utf16Name)),
	)

	if handle == 0 {
		return nil, fmt.Errorf("failed to open semaphore: %w", err)
	}

	return &windowsSemaphore{handle: windows.Handle(handle), name: name}, nil
}

func (s *windowsSemaphore) Acquire() error {
	ret, _, err := procWaitForSingleObject.Call(uintptr(s.handle), windows.INFINITE)
	if ret != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("failed to acquire semaphore: %w", err)
	}
	return nil
}

func (s *windowsSemaphore) Release() error {
	ret, _, err := procReleaseSemaphore.Call(uintptr(s.handle), 1, 0)
	if ret == 0 {
		return fmt.Errorf("failed to release semaphore: %w", err)
	}
	return nil
}

func (s *windowsSemaphore) TryAcquire() (bool, error) {
	ret, _, err := procWaitForSingleObject.Call(uintptr(s.handle), 0)
	switch ret {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uintptr(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, fmt.Errorf("error trying to acquire semaphore: %w", err)
	}
}

func (s *windowsSemaphore) AcquireTimeout(timeoutMs int) (bool, error) {
	ret, _, err := procWaitForSingleObject.Call(uintptr(s.handle), uintptr(timeoutMs))
	switch ret {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uintptr(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, fmt.Errorf("error acquiring semaphore with timeout: %w", err)
	}
}

func (s *windowsSemaphore) Close() error {
	ret, _, err := procCloseHandle.Call(uintptr(s.handle))
	if ret == 0 {
		return fmt.Errorf("failed to close semaphore: %w", err)
	}
	return nil
}
