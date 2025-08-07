package device

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/util"
)

type Engine struct {
	fetchSpecInterval  util.Duration
	fetchSpecFn        func(context.Context)
	pushStatusInterval util.Duration
	pushStatusFn       func(context.Context)

	clock Clock
	// startedCh is used to signal when the ticker has started used for testing
	startedCh chan struct{}
}

// NewEngine creates a new device engine.
func NewEngine(
	fetchSpecInterval util.Duration,
	fetchSpecFn func(context.Context),
	pushStatusInterval util.Duration,
	pushStatusFn func(context.Context),
) *Engine {
	return &Engine{
		fetchSpecInterval:  fetchSpecInterval,
		fetchSpecFn:        fetchSpecFn,
		pushStatusInterval: pushStatusInterval,
		pushStatusFn:       pushStatusFn,
		clock:              &realClock{},
		startedCh:          make(chan struct{}),
	}
}

func (e *Engine) calculateTickerInterval() time.Duration {
	var minInterval util.Duration
	if e.pushStatusInterval < e.fetchSpecInterval {
		minInterval = e.pushStatusInterval
	} else {
		minInterval = e.fetchSpecInterval
	}

	if minInterval <= 0 {
		// default to 1 second
		minInterval = util.Duration(1 * time.Second)
	}

	// return half of the min interval
	return time.Duration(minInterval / 2)
}

func (e *Engine) next(interval util.Duration, lastSync time.Time, now time.Time) bool {
	return now.Sub(lastSync) >= time.Duration(interval)
}

func (e *Engine) Run(ctx context.Context) error {
	// track the last time the spec and status were synced to ensure even distribution
	var lastSpecSync, lastStatusSync time.Time

	tickerInterval := e.calculateTickerInterval()
	timeTicker := e.clock.NewTicker(tickerInterval)
	defer timeTicker.Stop()

	// fire first spec sync immediately
	now := e.clock.Now()
	e.fetchSpecFn(ctx)
	lastSpecSync = now

	close(e.startedCh)
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
			if e.next(e.fetchSpecInterval, lastSpecSync, now) {
				lastSpecSync = now
				e.fetchSpecFn(ctx)
			}

			// status
			if e.next(e.pushStatusInterval, lastStatusSync, now) {
				lastStatusSync = now
				e.pushStatusFn(ctx)
			}
		}
	}
}

// Clock interface allows us to mock time in tests.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

// Tick is an interface that resembles time.Ticker.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// realClock is a Clock interface implementation that uses the real time package.
type realClock struct{}

func (r *realClock) Now() time.Time {
	return time.Now()
}

func (r *realClock) NewTicker(d time.Duration) Ticker {
	return &realTicker{time.NewTicker(d)}
}

type realTicker struct {
	*time.Ticker
}

func (r *realTicker) C() <-chan time.Time {
	return r.Ticker.C
}
