package pkg

import (
	"os"
	"sync/atomic"
	"syscall"
	"unsafe"
)

type SharedFIFO struct {
	Head     int64
	Tail     int64
	Count    int64
	Capacity int64
	Data     []byte
}

func NewSharedFIFO(filename string, capacity int64) (*SharedFIFO, error) {
	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	size := int64(unsafe.Sizeof(SharedFIFO{})) + capacity
	if err := file.Truncate(size); err != nil {
		return nil, err
	}

	data, err := syscall.Mmap(int(file.Fd()), 0, int(size), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}

	fifo := (*SharedFIFO)(unsafe.Pointer(&data[0]))
	fifo.Capacity = capacity
	fifo.Data = data[unsafe.Sizeof(SharedFIFO{}):]

	return fifo, nil
}

func (fifo *SharedFIFO) Push(item []byte) bool {
	for {
		tail := atomic.LoadInt64(&fifo.Tail)
		// head := atomic.LoadInt64(&fifo.Head)
		count := atomic.LoadInt64(&fifo.Count)

		if count >= fifo.Capacity {
			return false // Buffer is full
		}

		nextTail := (tail + 1) % fifo.Capacity

		if atomic.CompareAndSwapInt64(&fifo.Tail, tail, nextTail) {
			copy(fifo.Data[tail*int64(len(item)):], item)
			atomic.AddInt64(&fifo.Count, 1)
			return true
		}
	}
}

func (fifo *SharedFIFO) Pop(itemSize int) ([]byte, bool) {
	for {
		head := atomic.LoadInt64(&fifo.Head)
		count := atomic.LoadInt64(&fifo.Count)

		if count == 0 {
			return nil, false // Buffer is empty
		}

		nextHead := (head + 1) % fifo.Capacity

		if atomic.CompareAndSwapInt64(&fifo.Head, head, nextHead) {
			item := make([]byte, itemSize)
			copy(item, fifo.Data[head*int64(itemSize):])
			atomic.AddInt64(&fifo.Count, -1)
			return item, true
		}
	}
}
