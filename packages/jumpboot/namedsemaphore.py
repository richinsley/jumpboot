import ctypes
import time
import os
import sys

class NamedSemaphore:
    def __init__(self, name):
        self.name = name
        self.sem = None

        if os.name == 'nt':  # Windows
            self._init_windows()
        elif os.name == 'posix':  # POSIX (Linux, macOS)
            self._init_posix()
        else:
            raise OSError("Unsupported operating system")

    def _init_windows(self):
        from ctypes import wintypes

        self.kernel32 = ctypes.windll.kernel32
        
        self.OpenSemaphore = self.kernel32.OpenSemaphoreW
        self.OpenSemaphore.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.LPCWSTR]
        self.OpenSemaphore.restype = wintypes.HANDLE

        self.ReleaseSemaphore = self.kernel32.ReleaseSemaphore
        self.ReleaseSemaphore.argtypes = [wintypes.HANDLE, wintypes.LONG, ctypes.POINTER(wintypes.LONG)]
        self.ReleaseSemaphore.restype = wintypes.BOOL

        self.CloseHandle = self.kernel32.CloseHandle
        self.CloseHandle.argtypes = [wintypes.HANDLE]
        self.CloseHandle.restype = wintypes.BOOL

        self.WaitForSingleObject = self.kernel32.WaitForSingleObject
        self.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
        self.WaitForSingleObject.restype = wintypes.DWORD

        self.SEMAPHORE_ALL_ACCESS = 0x1F0003
        self.INFINITE = 0xFFFFFFFF

        self.sem = self.OpenSemaphore(self.SEMAPHORE_ALL_ACCESS, False, self.name)
        if self.sem == 0:
            raise ctypes.WinError()

    def _init_posix(self):
        if sys.platform == 'darwin':
            self.libc = ctypes.CDLL('libc.dylib')
        else:
            self.libc = ctypes.CDLL('libc.so.6')

        self.sem_open = self.libc.sem_open
        self.sem_open.argtypes = [ctypes.c_char_p, ctypes.c_int]
        self.sem_open.restype = ctypes.c_void_p

        self.sem_wait = self.libc.sem_wait
        self.sem_wait.argtypes = [ctypes.c_void_p]
        self.sem_wait.restype = ctypes.c_int

        self.sem_post = self.libc.sem_post
        self.sem_post.argtypes = [ctypes.c_void_p]
        self.sem_post.restype = ctypes.c_int

        self.sem_close = self.libc.sem_close
        self.sem_close.argtypes = [ctypes.c_void_p]
        self.sem_close.restype = ctypes.c_int

        self.sem = self.sem_open(self.name.encode(), 0)
        if self.sem == ctypes.c_void_p(-1).value:
            errno = ctypes.get_errno()
            raise OSError(errno, f"Failed to open semaphore: {os.strerror(errno)}")

    def acquire(self):
        if os.name == 'nt':
            result = self.WaitForSingleObject(self.sem, self.INFINITE)
            if result != 0:
                raise ctypes.WinError()
        else:
            if self.sem_wait(self.sem) != 0:
                errno = ctypes.get_errno()
                raise OSError(errno, f"Failed to acquire semaphore: {os.strerror(errno)}")

    def release(self):
        if os.name == 'nt':
            if not self.ReleaseSemaphore(self.sem, 1, None):
                raise ctypes.WinError()
        else:
            if self.sem_post(self.sem) != 0:
                errno = ctypes.get_errno()
                raise OSError(errno, f"Failed to release semaphore: {os.strerror(errno)}")

    def close(self):
        if os.name == 'nt':
            if not self.CloseHandle(self.sem):
                raise ctypes.WinError()
        else:
            if self.sem_close(self.sem) != 0:
                errno = ctypes.get_errno()
                raise OSError(errno, f"Failed to close semaphore: {os.strerror(errno)}")
