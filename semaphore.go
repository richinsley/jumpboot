package jumpboot

type Semaphore interface {
	Acquire() error
	Release() error
	TryAcquire() (bool, error)
	AcquireTimeout(timeoutMs int) (bool, error)
	Close() error
}
