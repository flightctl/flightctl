// retry_queue_test.go
package certmanager

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitWG(t *testing.T, ctx context.Context, wg *sync.WaitGroup, what string) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-ctx.Done():
		t.Fatalf("timeout waiting for %s: %v", what, ctx.Err())
	}
}

func TestRetryQueue_FIFO_NoRetry(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		got []int
	)

	wg.Add(3)

	q := newRetryQueue(func(ctx context.Context, item int, attempt int) *time.Duration {
		if attempt != 0 {
			t.Fatalf("unexpected attempt for item=%d: got %d, want 0", item, attempt)
		}
		mu.Lock()
		got = append(got, item)
		mu.Unlock()
		wg.Done()
		return nil
	})

	go q.RunWorker(ctx)

	q.Add(1)
	q.Add(2)
	q.Add(3)

	waitWG(t, ctx, &wg, "3 handler calls")

	mu.Lock()
	defer mu.Unlock()

	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("unexpected number of items: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FIFO violated: got=%v want=%v", got, want)
		}
	}
}

func TestRetryQueue_HandlerIsNotConcurrent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var (
		wg          sync.WaitGroup
		inHandler   int32
		maxParallel int32
	)

	// We expect exactly 3 handler calls.
	wg.Add(3)

	q := newRetryQueue(func(ctx context.Context, item int, attempt int) *time.Duration {
		// Track max parallelism.
		cur := atomic.AddInt32(&inHandler, 1)
		for {
			prev := atomic.LoadInt32(&maxParallel)
			if cur <= prev {
				break
			}
			if atomic.CompareAndSwapInt32(&maxParallel, prev, cur) {
				break
			}
		}

		// Simulate a bit of work.
		time.Sleep(10 * time.Millisecond)

		atomic.AddInt32(&inHandler, -1)
		wg.Done()
		return nil
	})

	go q.RunWorker(ctx)

	q.Add(1)
	q.Add(2)
	q.Add(3)

	waitWG(t, ctx, &wg, "3 handler calls")

	if atomic.LoadInt32(&maxParallel) != 1 {
		t.Fatalf("handler ran concurrently: maxParallel=%d (want 1)", maxParallel)
	}
}

func TestRetryQueue_Retry_AttemptIncrements(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		got []int
	)

	// We expect attempts 0,1,2.
	wg.Add(3)

	q := newRetryQueue(func(ctx context.Context, item string, attempt int) *time.Duration {
		mu.Lock()
		got = append(got, attempt)
		mu.Unlock()
		wg.Done()

		switch attempt {
		case 0, 1:
			// Use zero delay to avoid timing flakiness; retries should requeue immediately.
			d := time.Duration(0)
			return &d
		case 2:
			return nil
		default:
			t.Fatalf("unexpected attempt=%d", attempt)
			return nil
		}
	})

	go q.RunWorker(ctx)
	q.Add("x")

	waitWG(t, ctx, &wg, "attempts 0,1,2")

	mu.Lock()
	defer mu.Unlock()

	want := []int{0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("unexpected attempts: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attempt order mismatch: got=%v want=%v", got, want)
		}
	}
}

func TestRetryQueue_RetryDoesNotBlockOtherItems(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Expect:
	//   A:0 (schedules delayed retry)
	//   B:0 (should run before A:1)
	//   A:1
	const wantN = 4

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		got []string
	)

	wg.Add(wantN)

	q := newRetryQueue(func(ctx context.Context, item string, attempt int) *time.Duration {
		mu.Lock()
		got = append(got, fmt.Sprintf("%s:%d", item, attempt))
		mu.Unlock()
		wg.Done()

		if item == "A" && attempt == 0 {
			d := 50 * time.Millisecond
			return &d
		}

		if item == "B" && attempt == 0 {
			d := 10 * time.Millisecond
			return &d
		}
		return nil
	})

	go q.RunWorker(ctx)

	q.Add("A")
	q.Add("B")

	waitWG(t, ctx, &wg, "A:0, B:0, B:1, A:1")

	mu.Lock()
	defer mu.Unlock()

	want := []string{"A:0", "B:0", "B:1", "A:1"}
	if len(got) != len(want) {
		t.Fatalf("unexpected events: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch: got=%v want=%v", got, want)
		}
	}
}

func TestRetryQueue_NegativeDelay_TreatedAsZero(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// attempts 0 and 1 should happen quickly; attempt 0 returns negative delay.
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		got []int
	)

	wg.Add(2)

	q := newRetryQueue(func(ctx context.Context, item string, attempt int) *time.Duration {
		mu.Lock()
		got = append(got, attempt)
		mu.Unlock()
		wg.Done()

		if attempt == 0 {
			d := -1 * time.Second
			return &d
		}
		return nil
	})

	go q.RunWorker(ctx)
	q.Add("x")

	waitWG(t, ctx, &wg, "attempts 0 and 1")

	mu.Lock()
	defer mu.Unlock()

	want := []int{0, 1}
	if len(got) != len(want) {
		t.Fatalf("unexpected attempts: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attempt order mismatch: got=%v want=%v", got, want)
		}
	}
}

func TestRetryQueue_CancelStopsAndPreventsRequeue(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		firstCall sync.Once
		calls     int32
		wg        sync.WaitGroup
	)

	// We expect exactly 2 calls. It schedules a long retry, then we cancel.
	wg.Add(2)

	q := newRetryQueue(func(ctx context.Context, item string, attempt int) *time.Duration {
		atomic.AddInt32(&calls, 1)
		wg.Done()

		if item == "x" {
			d := 50 * time.Second
			return &d
		}

		// Schedule a retry far enough that we can cancel before it fires.
		d := 5 * time.Second
		firstCall.Do(func() {
			// Cancel right after first processing so the requeue goroutine should drop.
			cancel()
		})
		return &d
	})

	go q.RunWorker(ctx)
	q.Add("x")
	q.Add("y")

	// Wait for the first call to happen.
	waitWG(t, context.Background(), &wg, "second handler call")

	// Give a small window for any incorrect immediate requeue to show up.
	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected exactly 2 handler call after cancel; got %d", got)
	}
}
