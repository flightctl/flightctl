package device

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/stretchr/testify/require"
)

func TestEngineRun(t *testing.T) {
	require := require.New(t)

	startTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	mockClock := newMockClock(startTime)

	var (
		mu          sync.Mutex
		specCount   int
		statusCount int
	)

	syncSpecFn := func(ctx context.Context) {
		mu.Lock()
		defer mu.Unlock()
		specCount++
	}
	pushStatusFn := func(ctx context.Context) {
		mu.Lock()
		defer mu.Unlock()
		statusCount++
	}

	startCh := make(chan struct{})
	statusInterval := 60 * time.Second // Status every 60s
	engine := Engine{
		syncSpecFn:         syncSpecFn,
		pushStatusInterval: util.Duration(statusInterval),
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

	//initial sync happens immediately
	time.Sleep(10 * time.Millisecond) // give goroutine time to process tick
	mu.Lock()
	require.Equal(1, specCount, "initial spec sync should happen")
	require.Equal(1, statusCount, "initial status push should happen")
	mu.Unlock()

	for i := 0; i < 5; i++ {
		mockClock.Advance(1 * time.Second)
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	require.Equal(6, specCount, "spec should sync 5 more times (every 1s)")
	require.Equal(1, statusCount, "status should not push yet (60s interval)")
	mu.Unlock()

	for i := 0; i < 55; i++ {
		mockClock.Advance(1 * time.Second)
		time.Sleep(1 * time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	require.Equal(61, specCount, "spec should sync 55 more times")
	require.Equal(2, statusCount, "status should push at 60s")
	mu.Unlock()

	cancel()
}

func TestEngineStatusStartupJitter(t *testing.T) {
	require := require.New(t)

	startTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	mockClock := newMockClock(startTime)

	var (
		mu          sync.Mutex
		statusCount int
	)
	engine := Engine{
		syncSpecFn:         func(context.Context) {},
		pushStatusInterval: util.Duration(60 * time.Second),
		pushStatusFn: func(context.Context) {
			mu.Lock()
			defer mu.Unlock()
			statusCount++
		},
		statusStartupDelay: 30 * time.Second,
		clock:              mockClock,
		startedCh:          make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		require.NoError(engine.Run(ctx))
	}()
	<-engine.startedCh

	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	require.Equal(0, statusCount, "status should wait for startup jitter")
	mu.Unlock()

	mockClock.Advance(30 * time.Second)
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	require.Equal(1, statusCount, "status should push after jitter")
	mu.Unlock()

	mockClock.Advance(60 * time.Second)
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	require.Equal(2, statusCount, "status ticker should start after first push")
	mu.Unlock()
}

type mockClock struct {
	now    time.Time
	ticks  map[time.Duration]*mockTicker
	afters []mockAfter
}

type mockAfter struct {
	at time.Time
	ch chan time.Time
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

func (m *mockClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	at := m.now.Add(d)
	if !at.After(m.now) {
		ch <- m.now
		return ch
	}
	m.afters = append(m.afters, mockAfter{at: at, ch: ch})
	return ch
}

func (m *mockTicker) C() <-chan time.Time {
	return m.c
}

func (m *mockTicker) Stop() {
	close(m.c)
}

func (m *mockClock) Advance(d time.Duration) {
	m.now = m.now.Add(d)
	remaining := m.afters[:0]
	for _, a := range m.afters {
		if !a.at.After(m.now) {
			a.ch <- a.at
			continue
		}
		remaining = append(remaining, a)
	}
	m.afters = remaining
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
