package device

import (
	"context"
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

	clock Clock
	// startedCh is used to signal when the ticker has started used for testing
	startedCh chan struct{}
}

// NewEngine creates a new device engine.
func NewEngine(
	syncSpecFn func(context.Context),
	pushStatusInterval util.Duration,
	pushStatusFn func(context.Context),
) *Engine {
	return &Engine{
		syncSpecFn:         syncSpecFn,
		pushStatusInterval: pushStatusInterval,
		pushStatusFn:       pushStatusFn,
		clock:              &realClock{},
		startedCh:          make(chan struct{}),
	}
}

func (e *Engine) Run(ctx context.Context) error {
	specTicker := e.clock.NewTicker(specSyncInterval)
	defer specTicker.Stop()

	statusTicker := e.clock.NewTicker(time.Duration(e.pushStatusInterval))
	defer statusTicker.Stop()

	// sync immediately on startup
	e.syncSpecFn(ctx)
	e.pushStatusFn(ctx)

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
		case t := <-statusTicker.C():
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
