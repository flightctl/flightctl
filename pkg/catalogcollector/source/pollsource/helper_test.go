package pollsource_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/pollsource"
	"github.com/sirupsen/logrus"
)

// noJitter returns 0 for all inputs to make tests deterministic.
func noJitter(time.Duration) time.Duration { return 0 }

// fakeConsumer records the number of Consume calls and optionally fails.
type fakeConsumer struct {
	calls  atomic.Int64
	failN  int // fail the first N calls
	failed atomic.Int64
}

func (c *fakeConsumer) Consume(_ context.Context, _ *catalogcollector.CatalogSnapshot) error {
	n := int(c.calls.Add(1))
	if c.failN > 0 && n <= c.failN {
		c.failed.Add(1)
		return errors.New("consumer error")
	}
	return nil
}

var emptySnapshot = &catalogcollector.CatalogSnapshot{Revision: "test"}

func newLogger() *logrus.Entry {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)
	return l.WithField("test", true)
}

func ud(d time.Duration) util.Duration { return util.Duration(d) }

func defaultBackoff() pollsource.BackoffConfig {
	return pollsource.BackoffConfig{
		InitialInterval:     ud(10 * time.Millisecond),
		MaxInterval:         ud(50 * time.Millisecond),
		Multiplier:          2.0,
		RandomizationFactor: 0,
	}
}

func TestHelper_ImmediateFirstCollection(t *testing.T) {
	collected := make(chan struct{}, 1)
	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		collected <- struct{}{}
		return emptySnapshot, nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	consumer := &fakeConsumer{}
	h := pollsource.NewHelper("test", time.Minute, defaultBackoff(), newLogger(), nil, noJitter)

	done := make(chan error, 1)
	go func() {
		done <- h.Run(ctx, collect, consumer)
	}()

	select {
	case <-collected:
	case <-time.After(2 * time.Second):
		t.Fatal("first collection did not run immediately")
	}
	cancel()
	<-done
}

func TestHelper_CancellationDuringWait(t *testing.T) {
	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return emptySnapshot, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	consumer := &fakeConsumer{}

	h := pollsource.NewHelper("test", time.Hour, defaultBackoff(), newLogger(), nil, noJitter)

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- h.Run(ctx, collect, consumer)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	err := <-done
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if elapsed >= time.Second {
		t.Errorf("cancellation took too long: %v", elapsed)
	}
}

func TestHelper_BackoffAfterCollectFailure(t *testing.T) {
	var calls atomic.Int64
	collectErr := errors.New("collect error")

	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		calls.Add(1)
		return nil, collectErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	consumer := &fakeConsumer{}
	b := pollsource.BackoffConfig{
		InitialInterval:     ud(20 * time.Millisecond),
		MaxInterval:         ud(100 * time.Millisecond),
		Multiplier:          2.0,
		RandomizationFactor: 0,
	}
	h := pollsource.NewHelper("test", time.Minute, b, newLogger(), nil, noJitter)

	var failures atomic.Int64
	h.OnFailure = func(d time.Duration, err error) { failures.Add(1) }

	<-func() chan error {
		ch := make(chan error, 1)
		go func() { ch <- h.Run(ctx, collect, consumer) }()
		return ch
	}()

	// With 20ms initial and 2× multiplier: 20, 40, 80, 160 (capped at 100) ...
	// In 300ms we should see multiple calls.
	if n := calls.Load(); n < 2 {
		t.Errorf("expected at least 2 collection attempts in 300ms, got %d", n)
	}
	if n := failures.Load(); n == 0 {
		t.Error("expected OnFailure to be called")
	}
}

func TestHelper_BackoffResetAfterSuccess(t *testing.T) {
	var callCount atomic.Int64
	// Fail twice then succeed.
	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		n := callCount.Add(1)
		if n <= 2 {
			return nil, errors.New("transient error")
		}
		return emptySnapshot, nil
	}

	consumer := &fakeConsumer{}
	b := pollsource.BackoffConfig{
		InitialInterval:     ud(10 * time.Millisecond),
		MaxInterval:         ud(200 * time.Millisecond),
		Multiplier:          2.0,
		RandomizationFactor: 0,
	}

	var successCalled atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())

	h := pollsource.NewHelper("test", time.Hour, b, newLogger(), nil, noJitter)
	h.OnSuccess = func(d time.Duration) {
		successCalled.Store(true)
		cancel() // stop after first success
	}

	done := make(chan error, 1)
	go func() { done <- h.Run(ctx, collect, consumer) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("test timed out")
	}

	if !successCalled.Load() {
		t.Error("OnSuccess was not called")
	}
	if n := callCount.Load(); n < 3 {
		t.Errorf("expected at least 3 calls (2 failures + 1 success), got %d", n)
	}
}

func TestHelper_DownstreamConsumeFailureAppliesBackoff(t *testing.T) {
	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return emptySnapshot, nil
	}

	var successCount atomic.Int64
	consumer := &fakeConsumer{failN: 2} // first 2 consumes fail

	b := pollsource.BackoffConfig{
		InitialInterval:     ud(10 * time.Millisecond),
		MaxInterval:         ud(100 * time.Millisecond),
		Multiplier:          2.0,
		RandomizationFactor: 0,
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := pollsource.NewHelper("test", time.Hour, b, newLogger(), nil, noJitter)
	h.OnSuccess = func(d time.Duration) {
		if successCount.Add(1) >= 1 {
			cancel()
		}
	}

	done := make(chan error, 1)
	go func() { done <- h.Run(ctx, collect, consumer) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("test timed out waiting for success after consumer failures")
	}

	if n := consumer.calls.Load(); n < 3 {
		t.Errorf("expected at least 3 consumer calls (2 fail + 1 success), got %d", n)
	}
}

func TestHelper_NoOverlappingCycles(t *testing.T) {
	var inFlight atomic.Int64
	var overlap atomic.Bool

	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		if inFlight.Add(1) > 1 {
			overlap.Store(true)
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return emptySnapshot, nil
	}

	b := pollsource.BackoffConfig{
		InitialInterval:     ud(1 * time.Millisecond),
		MaxInterval:         ud(10 * time.Millisecond),
		Multiplier:          2.0,
		RandomizationFactor: 0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	consumer := &fakeConsumer{}
	h := pollsource.NewHelper("test", 1*time.Millisecond, b, newLogger(), nil, noJitter)
	h.Run(ctx, collect, consumer) //nolint:errcheck

	if overlap.Load() {
		t.Error("collection cycles overlapped")
	}
}

func TestHelper_CancellationDuringCollect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		cancel() // cancel during the first collect
		<-ctx.Done()
		return nil, ctx.Err()
	}

	consumer := &fakeConsumer{}
	h := pollsource.NewHelper("test", time.Minute, defaultBackoff(), newLogger(), nil, noJitter)

	err := h.Run(ctx, collect, consumer)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestHelper_MultiplierOne_PreservesInterval(t *testing.T) {
	// Regression test: with multiplier 1, the backoff interval must remain
	// at the initial value instead of jumping to maximum.
	//
	// The exact per-cycle interval is asserted by the same-package tests in
	// helper_internal_test.go; this test only confirms the end-to-end effect.
	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return nil, errors.New("always fail")
	}

	b := pollsource.BackoffConfig{
		InitialInterval:     ud(10 * time.Millisecond),
		MaxInterval:         ud(5 * time.Minute),
		Multiplier:          1.0, // no exponential growth
		RandomizationFactor: 0,
	}

	h := pollsource.NewHelper("test-mult1", 10*time.Millisecond, b, newLogger(), nil, noJitter)

	var failCount atomic.Int64
	h.OnFailure = func(d time.Duration, err error) {
		failCount.Add(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_ = h.Run(ctx, collect, &fakeConsumer{})

	// With multiplier 1, initial 10ms, and no jitter, the delay should
	// remain 10ms every cycle. In 200ms we should see ~10+ failures.
	// With the bug (jumping to 5min), we'd see only 1 failure.
	fails := failCount.Load()
	if fails < 5 {
		t.Errorf("expected at least 5 failures in 200ms with 10ms backoff, got %d (multiplier 1 likely jumped to max)", fails)
	}
}

func TestHelper_ResetAfterSuccess_ThenFailAgain(t *testing.T) {
	// Strengthen reset test: after success resets backoff, the next failure
	// must use the initial interval again, not a carried-over higher value.
	var callCount atomic.Int64

	// Cycle: fail, fail, succeed, fail, fail, succeed...
	// After the success, backoff resets. The second set of failures must
	// also start from InitialInterval.
	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		n := callCount.Add(1)
		phase := ((n - 1) % 3) // 0, 1, 2, 0, 1, 2, ...
		if phase < 2 {
			return nil, errors.New("transient")
		}
		return emptySnapshot, nil
	}

	b := pollsource.BackoffConfig{
		InitialInterval:     ud(5 * time.Millisecond),
		MaxInterval:         ud(200 * time.Millisecond),
		Multiplier:          2.0,
		RandomizationFactor: 0,
	}

	var successCount atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())

	h := pollsource.NewHelper("test-reset", 5*time.Millisecond, b, newLogger(), nil, noJitter)
	h.OnSuccess = func(d time.Duration) {
		if successCount.Add(1) >= 2 {
			cancel() // stop after second success
		}
	}

	done := make(chan error, 1)
	go func() { done <- h.Run(ctx, collect, &fakeConsumer{}) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("test timed out")
	}

	// We need at least 6 calls: fail, fail, success, fail, fail, success
	if n := callCount.Load(); n < 6 {
		t.Errorf("expected at least 6 calls (2 cycles of fail-fail-succeed), got %d", n)
	}
	if n := successCount.Load(); n < 2 {
		t.Errorf("expected at least 2 successes, got %d", n)
	}
}

func TestBackoffConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     pollsource.BackoffConfig
		wantErr bool
	}{
		{
			name: "valid",
			cfg: pollsource.BackoffConfig{
				InitialInterval: ud(time.Second), MaxInterval: ud(time.Minute),
				Multiplier: 2.0, RandomizationFactor: 0.5,
			},
		},
		{
			name:    "zero initial interval",
			cfg:     pollsource.BackoffConfig{InitialInterval: ud(0), MaxInterval: ud(time.Minute), Multiplier: 2.0},
			wantErr: true,
		},
		{
			name:    "initial > max",
			cfg:     pollsource.BackoffConfig{InitialInterval: ud(time.Minute), MaxInterval: ud(time.Second), Multiplier: 2.0},
			wantErr: true,
		},
		{
			name:    "multiplier < 1",
			cfg:     pollsource.BackoffConfig{InitialInterval: ud(time.Second), MaxInterval: ud(time.Minute), Multiplier: 0.5},
			wantErr: true,
		},
		{
			name:    "randomization > 1",
			cfg:     pollsource.BackoffConfig{InitialInterval: ud(time.Second), MaxInterval: ud(time.Minute), Multiplier: 2.0, RandomizationFactor: 1.1},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
