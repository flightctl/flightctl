package ring_buffer

import (
	"fmt"
	"sync"
)

type RingBuffer[T any] struct {
	data     []T
	capacity int
	start    int // read index
	end      int // write index
	size     int
	stopped  bool

	mu   sync.Mutex
	cond *sync.Cond
}

func NewRingBuffer[T any](cap int) *RingBuffer[T] {
	if cap <= 0 {
		panic("capacity must be > 0")
	}
	rb := &RingBuffer[T]{
		data:     make([]T, cap),
		capacity: cap,
	}
	rb.cond = sync.NewCond(&rb.mu)
	return rb
}

// Push adds a value, overwriting the oldest if full
func (rb *RingBuffer[T]) Push(val T) error {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.stopped {
		return fmt.Errorf("ring buffer is stopped")
	}
	if rb.size == rb.capacity {
		// buffer full → overwrite oldest
		rb.start = (rb.start + 1) % rb.capacity
	} else {
		rb.size++
	}

	rb.data[rb.end] = val
	rb.end = (rb.end + 1) % rb.capacity

	rb.cond.Signal()
	return nil
}

// Pop blocks until an item is available
func (rb *RingBuffer[T]) Pop() (T, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	var zero T

	if rb.stopped {
		return zero, fmt.Errorf("ring buffer is stopped")
	}

	for rb.size == 0 {
		rb.cond.Wait()
		if rb.stopped {
			return zero, fmt.Errorf("ring buffer is stopped")
		}
	}

	val := rb.data[rb.start]
	rb.data[rb.start] = zero // optional: clear
	rb.start = (rb.start + 1) % rb.capacity
	rb.size--
	return val, nil
}

// TryPop tries to get an item
func (rb *RingBuffer[T]) TryPop() (T, bool, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	var zero T
	if rb.stopped {
		return zero, false, fmt.Errorf("ring buffer is stopped")
	}

	if rb.size == 0 {
		return zero, false, nil
	}

	val := rb.data[rb.start]
	rb.data[rb.start] = zero // optional: clear
	rb.start = (rb.start + 1) % rb.capacity
	rb.size--
	return val, true, nil
}

func (rb *RingBuffer[T]) Stop() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.stopped = true
	rb.cond.Broadcast()
}
