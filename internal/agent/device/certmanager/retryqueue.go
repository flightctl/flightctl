package certmanager

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// HandlerFunc defines a processing function for each item.
// It receives the item and its current attempt number (0 on first try).
// It returns a *time.Duration:
//   - nil: drop the item, no requeue
//   - non-nil: requeue after given duration
type HandlerFunc[T any] func(ctx context.Context, item T, attempt int) *time.Duration

// RetryQueue represents a generic queue that processes items using a handler,
// supports delayed requeue, and stops gracefully on context cancellation.
type RetryQueue[T any] struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []queuedItem[T]
	handler HandlerFunc[T]
}

type queuedItem[T any] struct {
	value   T
	attempt int
}

// NewRetryQueue creates a new RetryQueue with the given handler function.
func NewRetryQueue[T any](handler HandlerFunc[T]) *RetryQueue[T] {
	q := &RetryQueue[T]{handler: handler}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Add inserts a new item into the queue with attempt 0.
func (q *RetryQueue[T]) Add(item T) {
	q.addWithAttempt(item, 0)
}

// RunWorker starts the worker loop to process items until context is canceled.
func (q *RetryQueue[T]) RunWorker(ctx context.Context) {
	// Extra goroutine to wake up the worker when context is canceled.
	go func() {
		<-ctx.Done()
		q.mu.Lock()
		q.cond.Broadcast()
		q.mu.Unlock()
	}()

	for {
		q.mu.Lock()
		for len(q.items) == 0 && ctx.Err() == nil {
			q.cond.Wait()
		}
		if ctx.Err() != nil {
			q.mu.Unlock()
			fmt.Println("Context canceled, stopping worker")
			return
		}
		item := q.items[0]
		q.items = q.items[1:]
		q.mu.Unlock()

		delay := q.handler(ctx, item.value, item.attempt)
		if delay != nil {
			go func(it queuedItem[T], d time.Duration) {
				time.Sleep(d)
				q.addWithAttempt(it.value, it.attempt+1)
			}(item, *delay)
		}
	}
}

func (q *RetryQueue[T]) addWithAttempt(item T, attempt int) {
	q.mu.Lock()
	q.items = append(q.items, queuedItem[T]{value: item, attempt: attempt})
	q.mu.Unlock()
	q.cond.Signal()
}
