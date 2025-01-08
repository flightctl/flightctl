package device

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/util"
)

type TaskType string

const (
	FetchSpec  TaskType = "FetchSpec"
	PushStatus TaskType = "PushStatus"
)

type Ticker struct {
	fetchSpecInterval  util.Duration
	fetchSpecFn        func(context.Context)
	pushStatusInterval util.Duration
	pushStatusFn       func(context.Context)

	clock    Clock
	cancelFn context.CancelFunc
	// startedCh is used to signal when the ticker has started used for testing
	startedCh chan struct{}
}

func NewTicker(
	fetchSpecInterval util.Duration,
	fetchSpecFn func(context.Context),
	pushStatusInterval util.Duration,
	pushStatusFn func(context.Context),
) *Ticker {
	return &Ticker{
		fetchSpecInterval:  fetchSpecInterval,
		fetchSpecFn:        fetchSpecFn,
		pushStatusInterval: pushStatusInterval,
		pushStatusFn:       pushStatusFn,
		clock:              &realClock{},
		startedCh:          make(chan struct{}),
	}
}

func (t *Ticker) calculateTickerInterval() time.Duration {
	smallestInterval := t.fetchSpecInterval
	if t.pushStatusInterval < t.fetchSpecInterval {
		smallestInterval = t.pushStatusInterval
	}

	// return half of the smallest interval
	return time.Duration(smallestInterval / 2)
}

func (t *Ticker) next(taskType TaskType, lastSync time.Time, now time.Time) bool {
	var interval util.Duration
	if taskType == FetchSpec {
		interval = t.fetchSpecInterval
	} else {
		interval = t.pushStatusInterval
	}

	return now.Sub(lastSync) >= time.Duration(interval)
}

func (t *Ticker) Run(ctx context.Context) error {
	ctx, t.cancelFn = context.WithCancel(ctx)

	// track the last time the spec and status were synced to ensure even distribution
	var lastSpecSync, lastStatusSync time.Time

	tickerInterval := t.calculateTickerInterval()
	timeTicker := t.clock.NewTicker(tickerInterval)
	defer timeTicker.Stop()
	close(t.startedCh)
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-timeTicker.C():
			if now.IsZero() {
				// clock was stopped
				return nil
			}
			// spec
			if t.next(FetchSpec, lastSpecSync, now) {
				lastSpecSync = now
				t.fetchSpecFn(ctx)
			}

			// status
			if t.next(PushStatus, lastStatusSync, now) {
				lastStatusSync = now
				t.pushStatusFn(ctx)
			}
		}
	}
}

func (t *Ticker) Stop() {
	t.cancelFn()
}

// Clock interface allows us to mock time in tests.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Tick
}

// Tick is an interface that resembles time.Ticker.
type Tick interface {
	C() <-chan time.Time
	Stop()
}

// realClock is a Clock interface implementation that uses the real time package.
type realClock struct{}

func (r *realClock) Now() time.Time {
	return time.Now()
}

func (r *realClock) NewTicker(d time.Duration) Tick {
	return &realTicker{time.NewTicker(d)}
}

type realTicker struct {
	*time.Ticker
}

func (r *realTicker) C() <-chan time.Time {
	return r.Ticker.C
}
