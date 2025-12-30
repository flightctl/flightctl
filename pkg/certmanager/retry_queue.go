package certmanager

import (
	"container/heap"
	"context"
	"sync"
	"time"
)

// HandlerFunc defines a processing function for each item in the retry queue.
//
// attempt is 0 on the first invocation.
//
// Return value semantics:
//   - nil:     processing is complete; do not retry
//   - non-nil: requeue the item after the returned delay (<=0 means immediate)
type HandlerFunc[T any] func(ctx context.Context, item T, attempt int) *time.Duration

// RetryQueue processes items sequentially using a handler function and supports delayed retries.
// Single worker goroutine; no goroutine-per-retry.
type RetryQueue[T any] struct {
	mu      sync.Mutex
	pq      priorityQueue[T]
	seq     uint64
	handler HandlerFunc[T]

	// coalescing wake-up signal for RunWorker when Add() is called
	wakeup chan struct{}
}

// queuedItem represents a queued value along with its retry attempt count and due time.
type queuedItem[T any] struct {
	value   T
	attempt int
	due     time.Time
	seq     uint64 // FIFO tie-breaker when due is equal
	index   int    // heap index
}

// priorityQueue is a min-heap ordered by due time, then FIFO (seq).
type priorityQueue[T any] []*queuedItem[T]

func (pq priorityQueue[T]) Len() int { return len(pq) }

func (pq priorityQueue[T]) Less(i, j int) bool {
	if pq[i].due.Equal(pq[j].due) {
		return pq[i].seq < pq[j].seq
	}
	return pq[i].due.Before(pq[j].due)
}

func (pq priorityQueue[T]) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue[T]) Push(x any) {
	it := x.(*queuedItem[T])
	it.index = len(*pq)
	*pq = append(*pq, it)
}

func (pq *priorityQueue[T]) Pop() any {
	// NOTE: heap.Pop calls our Pop after it has moved the root to the end.
	// So "end of slice" is the element being removed.
	old := *pq
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	it.index = -1
	*pq = old[:n-1]
	return it
}

// newRetryQueue creates a new RetryQueue with the given handler.
func newRetryQueue[T any](handler HandlerFunc[T]) *RetryQueue[T] {
	q := &RetryQueue[T]{
		handler: handler,
		wakeup:  make(chan struct{}, 1), // coalescing signal
	}
	heap.Init(&q.pq)
	return q
}

// Add inserts a new item into the queue with attempt 0, due immediately.
func (q *RetryQueue[T]) Add(item T) {
	q.addWithAttempt(item, 0, time.Now())
}

func (q *RetryQueue[T]) addWithAttempt(item T, attempt int, due time.Time) {
	q.mu.Lock()
	q.seq++
	heap.Push(&q.pq, &queuedItem[T]{
		value:   item,
		attempt: attempt,
		due:     due,
		seq:     q.seq,
	})
	q.mu.Unlock()

	// Wake worker (coalesce).
	select {
	case q.wakeup <- struct{}{}:
	default:
	}
}

// RunWorker runs the retry queue worker loop until ctx is canceled.
// Items are processed sequentially. Retries are scheduled via heap + a single timer.
func (q *RetryQueue[T]) RunWorker(ctx context.Context) {
	var (
		timer   = time.NewTimer(time.Hour) // placeholder; we stop immediately
		timerCh <-chan time.Time
	)
	timer.Stop()
	timerCh = nil // disabled until we have items

	stopAndDrain := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}

	resetTimer := func(d time.Duration) {
		stopAndDrain()
		timer.Reset(d)
		timerCh = timer.C
	}

	disableTimer := func() {
		stopAndDrain()
		timerCh = nil
	}

	peekDue := func() (time.Time, bool) {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.pq.Len() == 0 {
			return time.Time{}, false
		}
		return q.pq[0].due, true // root is earliest due
	}

	popReady := func(now time.Time) *queuedItem[T] {
		q.mu.Lock()
		defer q.mu.Unlock()

		if q.pq.Len() == 0 {
			return nil
		}
		if q.pq[0].due.After(now) {
			return nil
		}
		return heap.Pop(&q.pq).(*queuedItem[T])
	}

	// arm timer if items already exist
	if due, ok := peekDue(); ok {
		resetTimer(max(time.Until(due), time.Duration(0)))
	}

	for {
		select {
		case <-ctx.Done():
			disableTimer()
			return

		case <-q.wakeup:
			// new item may have changed earliest due
			if due, ok := peekDue(); ok {
				resetTimer(max(time.Until(due), time.Duration(0)))
			} else {
				disableTimer()
			}

		case <-timerCh:
			// process all ready items (burst-safe)
			now := time.Now()
			for {
				it := popReady(now)
				if it == nil {
					break
				}

				delay := q.handler(ctx, it.value, it.attempt)
				if delay != nil && ctx.Err() == nil {
					nextDelay := max(*delay, time.Duration(0))
					q.addWithAttempt(it.value, it.attempt+1, time.Now().Add(nextDelay))
				}
			}

			// schedule next due (or disable timer)
			if due, ok := peekDue(); ok {
				resetTimer(max(time.Until(due), time.Duration(0)))
			} else {
				disableTimer()
			}
		}
	}
}
