package device

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/stretchr/testify/require"
)

type testTaskType string

const (
	fetchSpec  testTaskType = "FetchSpec"
	pushStatus testTaskType = "PushStatus"
)

func TestEngineDistribution(t *testing.T) {
	require := require.New(t)

	startTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	mockClock := newMockClock(startTime)

	var (
		mu     sync.Mutex
		events []testTaskType
	)

	fetchSpecFn := func(ctx context.Context) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, fetchSpec)
	}
	pushStatusFn := func(ctx context.Context) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, pushStatus)
	}

	startCh := make(chan struct{})
	fetchSpecInterval := 100 * time.Millisecond
	fetchStatusInterval := 100 * time.Millisecond
	engine := Engine{
		fetchSpecInterval:  util.Duration(fetchSpecInterval),
		fetchSpecFn:        fetchSpecFn,
		pushStatusInterval: util.Duration(fetchStatusInterval),
		pushStatusFn:       pushStatusFn,
		clock:              mockClock,
		startedCh:          startCh,
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		err := engine.Run(ctx)
		require.NoError(err)
	}()
	<-startCh // wait for ticker to start

	// simulate time passing 1000 intervals of 100ms
	for i := 0; i < 1000; i++ {
		// advance time +100 ms,
		mockClock.Advance(100 * time.Millisecond)
	}

	cancel()
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(events); i++ {
		require.NotEqual(events[i], events[i-1], "sequence must alternate")
	}
}

type mockClock struct {
	now   time.Time
	ticks map[time.Duration]*mockTicker
}

type mockTicker struct {
	c          chan time.Time
	d          time.Duration
	nextTickAt time.Time // next time to send into c channel.
	clock      *mockClock
}

func newMockClock(start time.Time) *mockClock {
	return &mockClock{
		now:   start,
		ticks: make(map[time.Duration]*mockTicker),
	}
}

func (m *mockClock) Now() time.Time {
	return m.now
}

// NewTicker creates a a mock ticker, storing its nextTickAt as now + duration.
func (m *mockClock) NewTicker(d time.Duration) Ticker {
	t := &mockTicker{
		c:          make(chan time.Time, 1),
		d:          d,
		nextTickAt: m.now.Add(d),
		clock:      m,
	}
	m.ticks[d] = t
	return t
}

func (m *mockTicker) C() <-chan time.Time {
	return m.c
}

func (m *mockTicker) Stop() {
	close(m.c)
}

func (m *mockClock) Advance(d time.Duration) {
	m.now = m.now.Add(d)
	// check how many intervals (dur) we crossed,
	// send ticks for each crossing.
	for dur, t := range m.ticks {
		for !t.nextTickAt.After(m.now) {
			// send a tick
			t.c <- t.nextTickAt
			// move forward by its interval.
			t.nextTickAt = t.nextTickAt.Add(dur)
		}
	}
}
