package device

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/flightctl/flightctl/internal/util"
)

const (
	// specSyncInterval is how frequently we check for pending spec changes.
	specSyncInterval = 1 * time.Second
)

type Engine struct {
	syncSpecFn         func(context.Context)
	pushStatusInterval util.Duration
	pushStatusFn       func(context.Context)

	// statusStartupDelay waits this long after Run starts before the first
	// status push (and before the status ticker). NewEngine picks a random
	// value in [0, pushStatusInterval) to desynchronize fleets after restart.
	statusStartupDelay time.Duration

	clock Clock
	// startedCh is used to signal when the ticker has started used for testing
	startedCh chan struct{}
}

// NewEngine creates a new device engine.
// statusJitterMax is the maximum random delay before the first status push
// (uniform in [0, statusJitterMax)). Zero disables jitter.
func NewEngine(
	syncSpecFn func(context.Context),
	pushStatusInterval util.Duration,
	pushStatusFn func(context.Context),
	statusJitterMax time.Duration,
) *Engine {
	var startupDelay time.Duration
	if statusJitterMax > 0 {
		startupDelay = time.Duration(rand.Int64N(int64(statusJitterMax))) //nolint:gosec // G404: status-push jitter
	}
	return &Engine{
		syncSpecFn:         syncSpecFn,
		pushStatusInterval: pushStatusInterval,
		pushStatusFn:       pushStatusFn,
		statusStartupDelay: startupDelay,
		clock:              &realClock{},
		startedCh:          make(chan struct{}),
	}
}

func (e *Engine) Run(ctx context.Context) error {
	specTicker := e.clock.NewTicker(specSyncInterval)
	defer specTicker.Stop()

	statusInterval := time.Duration(e.pushStatusInterval)
	var statusTicker Ticker
	defer func() {
		if statusTicker != nil {
			statusTicker.Stop()
		}
	}()

	// Spec sync immediately; status is delayed by statusStartupDelay so mass
	// agent restarts do not align every status push on the same second.
	e.syncSpecFn(ctx)

	var statusCh <-chan time.Time
	var firstStatusCh <-chan time.Time
	if e.statusStartupDelay <= 0 {
		e.pushStatusFn(ctx)
		statusTicker = e.clock.NewTicker(statusInterval)
		statusCh = statusTicker.C()
	} else {
		firstStatusCh = e.clock.After(e.statusStartupDelay)
	}

	close(e.startedCh)
	for {
		select {
		case <-ctx.Done():
			return nil
		case t := <-specTicker.C():
			if t.IsZero() {
				return nil
			}
			e.syncSpecFn(ctx)
		case <-firstStatusCh:
			firstStatusCh = nil
			e.pushStatusFn(ctx)
			statusTicker = e.clock.NewTicker(statusInterval)
			statusCh = statusTicker.C()
		case t := <-statusCh:
			if t.IsZero() {
				return nil
			}
			e.pushStatusFn(ctx)
		}
	}
}

// Clock interface allows us to mock time in tests.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
	After(d time.Duration) <-chan time.Time
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

func (r *realClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

type realTicker struct {
	*time.Ticker
}

func (r *realTicker) C() <-chan time.Time {
	return r.Ticker.C
}
