package certmanager

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// HandlerFunc defines a processing function for each item in the retry queue.
// It receives the item and its current attempt number (0 on first try).
// It returns a *time.Duration:
//   - nil: drop the item, no requeue (processing complete or permanently failed)
//   - non-nil: requeue after given duration (temporary failure, retry needed)
type HandlerFunc[T any] func(ctx context.Context, item T, attempt int) *time.Duration

// RetryQueue represents a generic queue that processes items using a handler,
// supports delayed requeue for failed operations, and stops gracefully on context cancellation.
// It provides at-least-once delivery semantics with exponential backoff capabilities.
type RetryQueue[T any] struct {
	mu      sync.Mutex      // Mutex for thread-safe access to queue state
	cond    *sync.Cond      // Condition variable for signaling queue state changes
	items   []queuedItem[T] // Queue of items awaiting processing
	handler HandlerFunc[T]  // Handler function to process items
}

// queuedItem represents an item in the retry queue with its attempt count.
// The attempt count is used to implement exponential backoff or attempt-based logic.
type queuedItem[T any] struct {
	value   T   // The item to be processed
	attempt int // Number of processing attempts (0 for first attempt)
}

// NewRetryQueue creates a new RetryQueue with the given handler function.
// The handler will be called for each item in the queue and can control retry behavior.
func NewRetryQueue[T any](handler HandlerFunc[T]) *RetryQueue[T] {
	q := &RetryQueue[T]{handler: handler}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Add inserts a new item into the queue with attempt 0.
// This is the main entry point for adding items to be processed.
func (q *RetryQueue[T]) Add(item T) {
	q.addWithAttempt(item, 0)
}

// RunWorker starts the worker loop to process items until context is canceled.
// This method blocks and should be run in a goroutine. It processes items sequentially
// and handles retry logic based on handler return values.
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

// addWithAttempt adds an item to the queue with a specific attempt count.
// This is used internally for retry logic and by the Add method for initial items.
func (q *RetryQueue[T]) addWithAttempt(item T, attempt int) {
	q.mu.Lock()
	q.items = append(q.items, queuedItem[T]{value: item, attempt: attempt})
	q.mu.Unlock()
	q.cond.Signal()
}
