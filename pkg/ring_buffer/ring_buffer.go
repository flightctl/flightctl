package ring_buffer

import (
	"fmt"
	"sync"
)

type RingBuffer[T any] struct {
	data     []T
	capacity int
	head     int // read index
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

	tail := (rb.head + rb.size) % rb.capacity
	rb.data[tail] = val

	if rb.size == rb.capacity {
		// buffer was full → oldest was overwritten. Move head to next item
		rb.head = (rb.head + 1) % rb.capacity
	} else {
		rb.size++
	}

	rb.cond.Signal()
	return nil
}

func (rb *RingBuffer[T]) popHead() T {
	var zero T
	val := rb.data[rb.head]
	rb.data[rb.head] = zero
	rb.head = (rb.head + 1) % rb.capacity
	rb.size--
	return val
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
	return rb.popHead(), nil
}

// TryPop tries to get an item without blocking
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

	return rb.popHead(), true, nil
}

func (rb *RingBuffer[T]) Stop() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.stopped = true
	rb.cond.Broadcast()
}
