package pollsource

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
)

// These tests live in the pollsource package so that the backoff arithmetic
// can be asserted directly instead of being inferred from wall-clock timing.
// No testing-only hook is exported from the production API: the loop tests
// below observe delay decisions through the existing NowFunc/JitterFunc
// injection points that NewHelper already accepts.

func internalLogger() *logrus.Entry {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	return logger.WithField("test", true)
}

func newInternalHelper(backoff BackoffConfig, jitter JitterFunc) *Helper {
	return NewHelper("internal", time.Millisecond, backoff, internalLogger(), nil, jitter)
}

// --- advance ---------------------------------------------------------------

// TestAdvance_IntervalGrowth asserts the exact next base interval produced by
// one failed cycle, including the point at which growth is capped.
func TestAdvance_IntervalGrowth(t *testing.T) {
	const maximum = 100 * time.Millisecond

	cases := []struct {
		name       string
		multiplier float64
		current    time.Duration
		want       time.Duration
	}{
		{
			name:       "when below the cap it should multiply",
			multiplier: 2,
			current:    10 * time.Millisecond,
			want:       20 * time.Millisecond,
		},
		{
			name:       "when the next step still fits it should multiply",
			multiplier: 2,
			current:    40 * time.Millisecond,
			want:       80 * time.Millisecond,
		},
		{
			name:       "when the next step would exceed the cap it should clamp",
			multiplier: 2,
			current:    60 * time.Millisecond,
			want:       maximum,
		},
		{
			name:       "when already at the cap it should stay there",
			multiplier: 2,
			current:    maximum,
			want:       maximum,
		},
		{
			name:       "when above the cap it should clamp back down",
			multiplier: 2,
			current:    5 * maximum,
			want:       maximum,
		},
		{
			name:       "when the multiplier is 1 it should preserve the interval",
			multiplier: 1,
			current:    10 * time.Millisecond,
			want:       10 * time.Millisecond,
		},
		{
			name:       "when the multiplier is fractional it should still grow",
			multiplier: 1.5,
			current:    10 * time.Millisecond,
			want:       15 * time.Millisecond,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			helper := newInternalHelper(BackoffConfig{
				InitialInterval:     util.Duration(time.Millisecond),
				MaxInterval:         util.Duration(maximum),
				Multiplier:          tc.multiplier,
				RandomizationFactor: 0,
			}, nil)

			if got := helper.advance(tc.current); got != tc.want {
				t.Errorf("advance(%s) = %s, want %s", tc.current, got, tc.want)
			}
		})
	}
}

// TestAdvance_MultiplierOneNeverReachesMaximum walks many consecutive
// failures with multiplier 1 and asserts the interval never drifts.
func TestAdvance_MultiplierOneNeverReachesMaximum(t *testing.T) {
	helper := newInternalHelper(BackoffConfig{
		InitialInterval:     util.Duration(10 * time.Millisecond),
		MaxInterval:         util.Duration(5 * time.Minute),
		Multiplier:          1,
		RandomizationFactor: 0,
	}, nil)

	current := 10 * time.Millisecond
	for i := 0; i < 50; i++ {
		current = helper.advance(current)
		if current != 10*time.Millisecond {
			t.Fatalf("advance drifted to %s after %d failures, want 10ms", current, i+1)
		}
	}
}

// TestAdvance_OverflowSafety asserts that a multiplication which would
// overflow time.Duration is capped instead of wrapping to a negative or tiny
// interval.
func TestAdvance_OverflowSafety(t *testing.T) {
	const maxDuration = time.Duration(math.MaxInt64)

	cases := []struct {
		name       string
		multiplier float64
		maximum    time.Duration
		current    time.Duration
	}{
		{
			name:       "when the product would overflow Duration it should cap",
			multiplier: 100,
			maximum:    maxDuration,
			current:    time.Duration(math.MaxInt64 / 10),
		},
		{
			name:       "when the multiplier is enormous it should cap",
			multiplier: 1e300,
			maximum:    maxDuration,
			current:    time.Second,
		},
		{
			name:       "when the current interval is already huge it should cap",
			multiplier: 2,
			maximum:    maxDuration,
			current:    maxDuration - 1,
		},
		{
			name:       "when a small cap is configured it should cap",
			multiplier: 1e18,
			maximum:    time.Second,
			current:    time.Millisecond,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			helper := newInternalHelper(BackoffConfig{
				InitialInterval:     util.Duration(time.Millisecond),
				MaxInterval:         util.Duration(tc.maximum),
				Multiplier:          tc.multiplier,
				RandomizationFactor: 0,
			}, nil)

			got := helper.advance(tc.current)

			if got <= 0 {
				t.Fatalf("advance(%s) = %s, want a positive interval", tc.current, got)
			}
			if got > tc.maximum {
				t.Fatalf("advance(%s) = %s, want at most %s", tc.current, got, tc.maximum)
			}
			if got < tc.current && tc.current <= tc.maximum {
				t.Fatalf("advance(%s) = %s, backoff must never shrink", tc.current, got)
			}
		})
	}
}

// --- withJitter ------------------------------------------------------------

// TestWithJitter_Boundaries asserts the exact delay produced for the boundary
// values the injected jitter source can return.
func TestWithJitter_Boundaries(t *testing.T) {
	const maximum = 100 * time.Millisecond

	cases := []struct {
		name   string
		factor float64
		base   time.Duration
		jitter JitterFunc
		want   time.Duration
	}{
		{
			name:   "when randomization is disabled it should use the base interval",
			factor: 0,
			base:   10 * time.Millisecond,
			want:   10 * time.Millisecond,
		},
		{
			name:   "when randomization is disabled it should still respect the cap",
			factor: 0,
			base:   5 * maximum,
			want:   maximum,
		},
		{
			name:   "when the jitter source returns zero it should use the lower bound",
			factor: 0.5,
			base:   10 * time.Millisecond,
			jitter: func(time.Duration) time.Duration { return 0 },
			want:   5 * time.Millisecond,
		},
		{
			name:   "when the jitter source returns the maximum offset it should stay below the upper bound",
			factor: 0.5,
			base:   10 * time.Millisecond,
			jitter: func(width time.Duration) time.Duration { return width - 1 },
			want:   15*time.Millisecond - 1,
		},
		{
			name:   "when the jitter source overshoots it should be clamped",
			factor: 0.5,
			base:   10 * time.Millisecond,
			jitter: func(width time.Duration) time.Duration { return width * 10 },
			want:   15*time.Millisecond - 1,
		},
		{
			name:   "when the jitter source is negative it should be clamped to the lower bound",
			factor: 0.5,
			base:   10 * time.Millisecond,
			jitter: func(time.Duration) time.Duration { return -time.Hour },
			want:   5 * time.Millisecond,
		},
		{
			name:   "when randomization is full it should keep the delay positive",
			factor: 1,
			base:   10 * time.Millisecond,
			jitter: func(time.Duration) time.Duration { return 0 },
			want:   time.Nanosecond,
		},
		{
			name:   "when the base is at the cap it should not exceed the cap",
			factor: 0.5,
			base:   maximum,
			jitter: func(width time.Duration) time.Duration { return width - 1 },
			want:   maximum - 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			helper := newInternalHelper(BackoffConfig{
				InitialInterval:     util.Duration(time.Millisecond),
				MaxInterval:         util.Duration(maximum),
				Multiplier:          2,
				RandomizationFactor: tc.factor,
			}, tc.jitter)

			got := helper.withJitter(tc.base)

			if got != tc.want {
				t.Errorf("withJitter(%s) = %s, want %s", tc.base, got, tc.want)
			}
			if got < time.Nanosecond || got > maximum {
				t.Errorf("withJitter(%s) = %s, outside [1ns, %s]", tc.base, got, maximum)
			}
		})
	}
}

// TestWithJitter_StaysWithinBounds sweeps the full offset range for a range of
// base intervals and asserts the delay never leaves the configured window.
func TestWithJitter_StaysWithinBounds(t *testing.T) {
	const maximum = 100 * time.Millisecond

	for _, factor := range []float64{0, 0.1, 0.5, 0.9, 1} {
		for _, base := range []time.Duration{
			time.Nanosecond,
			time.Microsecond,
			10 * time.Millisecond,
			maximum,
			10 * maximum,
		} {
			for _, offsetRatio := range []float64{0, 0.5, 1} {
				helper := newInternalHelper(BackoffConfig{
					InitialInterval:     util.Duration(time.Nanosecond),
					MaxInterval:         util.Duration(maximum),
					Multiplier:          2,
					RandomizationFactor: factor,
				}, func(width time.Duration) time.Duration {
					return time.Duration(float64(width) * offsetRatio)
				})

				got := helper.withJitter(base)
				if got < time.Nanosecond || got > maximum {
					t.Fatalf(
						"withJitter(base=%s, factor=%g, offsetRatio=%g) = %s, outside [1ns, %s]",
						base, factor, offsetRatio, got, maximum,
					)
				}
			}
		}
	}
}

// --- loop behaviour --------------------------------------------------------

// baseRecorder observes the base interval the helper decided to wait on.
//
// With RandomizationFactor 0.5 and a maximum interval of at least 1.5x the
// largest base, withJitter computes lower=base/2 and upper=1.5*base, so the
// width handed to the jitter source is exactly the current base interval. The
// recorder therefore captures the helper's actual delay decisions rather than
// measuring elapsed time.
//
// The recorder also stops the loop once it has observed the expected number of
// decisions. Cancelling from inside the collection callback would instead make
// Run exit before the final backoff decision is taken.
type baseRecorder struct {
	mu    sync.Mutex
	bases []time.Duration

	limit int
	stop  context.CancelFunc
}

func (r *baseRecorder) jitter(width time.Duration) time.Duration {
	r.mu.Lock()
	r.bases = append(r.bases, width)
	reached := r.limit > 0 && len(r.bases) >= r.limit
	r.mu.Unlock()

	if reached && r.stop != nil {
		r.stop()
	}
	return 0
}

func (r *baseRecorder) snapshot() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.bases...)
}

// observableBackoff returns a configuration whose jitter width equals the
// current base interval for every step of the growth sequence below.
func observableBackoff() BackoffConfig {
	return BackoffConfig{
		InitialInterval:     util.Duration(time.Millisecond),
		MaxInterval:         util.Duration(64 * time.Millisecond),
		Multiplier:          2,
		RandomizationFactor: 0.5,
	}
}

func assertBases(t *testing.T, got, want []time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("backoff base sequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoff base sequence = %v, want %v", got, want)
		}
	}
}

type scriptedConsumer struct {
	mu        sync.Mutex
	err       error
	calls     int
	failed    int
	succeeded int
}

func (c *scriptedConsumer) Consume(
	_ context.Context,
	_ *catalogcollector.CatalogSnapshot,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		c.failed++
		return c.err
	}
	c.succeeded++
	return nil
}

// completedCycles reports how many cycles finished both collection and
// downstream consumption successfully. Cycle outcomes are observed here rather
// than through a helper callback, because the helper reports collection
// outcomes only.
func (c *scriptedConsumer) completedCycles() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.succeeded
}

var testSnapshot = &catalogcollector.CatalogSnapshot{Revision: "internal-test"}

// TestRun_CollectionFailureGrowsBackoff asserts the exact sequence of base
// intervals the helper waits on while collection keeps failing.
func TestRun_CollectionFailureGrowsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &baseRecorder{limit: 5, stop: cancel}

	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return nil, errors.New("collection failed")
	}

	helper := NewHelper(
		"growth", time.Millisecond, observableBackoff(),
		internalLogger(), nil, recorder.jitter,
	)

	_ = helper.Run(ctx, collect, &scriptedConsumer{})

	assertBases(t, recorder.snapshot(), []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		4 * time.Millisecond,
		8 * time.Millisecond,
		16 * time.Millisecond,
	})
}

// TestRun_NilSnapshotGrowsBackoff asserts that a collector returning no
// snapshot and no error is treated as a failed cycle that advances backoff.
func TestRun_NilSnapshotGrowsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &baseRecorder{limit: 3, stop: cancel}

	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return nil, nil
	}

	helper := NewHelper(
		"nil-snapshot", time.Millisecond, observableBackoff(),
		internalLogger(), nil, recorder.jitter,
	)

	_ = helper.Run(ctx, collect, &scriptedConsumer{})

	assertBases(t, recorder.snapshot(), []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		4 * time.Millisecond,
	})
}

// TestRun_DownstreamFailureContinuesBackoff asserts that a successful
// collection followed by a failing consumer still advances backoff, and that
// backoff keeps growing for as long as the downstream keeps failing.
func TestRun_DownstreamFailureContinuesBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &baseRecorder{limit: 4, stop: cancel}

	consumer := &scriptedConsumer{err: errors.New("downstream rejected the snapshot")}

	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return testSnapshot, nil
	}

	var collectFailures int
	helper := NewHelper(
		"downstream", time.Millisecond, observableBackoff(),
		internalLogger(), nil, recorder.jitter,
	)
	helper.OnCollect = func(_ time.Duration, err error) {
		if err != nil {
			collectFailures++
		}
	}

	_ = helper.Run(ctx, collect, consumer)

	assertBases(t, recorder.snapshot(), []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		4 * time.Millisecond,
		8 * time.Millisecond,
	})
	if cycles := consumer.completedCycles(); cycles != 0 {
		t.Errorf("completed cycles = %d, want 0 while the downstream keeps failing", cycles)
	}
	if collectFailures != 0 {
		t.Errorf(
			"OnCollect reported %d collection failures, want 0; every collection succeeded",
			collectFailures,
		)
	}
	if consumer.failed != consumer.calls || consumer.calls == 0 {
		t.Errorf("consumer calls = %d, failures = %d", consumer.calls, consumer.failed)
	}
}

// TestRun_BackoffResetsOnlyAfterCollectionAndConsumptionSucceed asserts that
// backoff is reset exactly once both halves of a cycle succeed, and that the
// next failure restarts from the initial interval rather than from the
// carried-over value.
func TestRun_BackoffResetsOnlyAfterCollectionAndConsumptionSucceed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &baseRecorder{limit: 5, stop: cancel}

	consumeErr := errors.New("downstream rejected the snapshot")
	consumer := &scriptedConsumer{}

	// Cycle plan:
	//   1: collection fails              → base 1ms
	//   2: collection fails              → base 2ms
	//   3: collection succeeds, consume fails → base 4ms (no reset)
	//   4: collection succeeds, consume succeeds → reset
	//   5: collection fails              → base 1ms again
	//   6: collection fails              → base 2ms again
	attempts := 0
	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		attempts++

		consumer.mu.Lock()
		switch attempts {
		case 3:
			consumer.err = consumeErr
		case 4:
			consumer.err = nil
		}
		consumer.mu.Unlock()

		switch attempts {
		case 1, 2, 5, 6:
			return nil, errors.New("collection failed")
		default:
			return testSnapshot, nil
		}
	}

	// Collection outcomes are observed through OnCollect; completed cycles are
	// observed at the consumer. Backoff decisions are observed through the
	// injected jitter source.
	var collectFailures, collectSuccesses int
	helper := NewHelper(
		"reset", time.Millisecond, observableBackoff(),
		internalLogger(), nil, recorder.jitter,
	)
	helper.OnCollect = func(_ time.Duration, err error) {
		if err != nil {
			collectFailures++
			return
		}
		collectSuccesses++
	}

	_ = helper.Run(ctx, collect, consumer)

	assertBases(t, recorder.snapshot(), []time.Duration{
		1 * time.Millisecond, // cycle 1: collection failed
		2 * time.Millisecond, // cycle 2: collection failed
		4 * time.Millisecond, // cycle 3: collection succeeded, consumption failed
		1 * time.Millisecond, // cycle 5: reset took effect after cycle 4
		2 * time.Millisecond, // cycle 6
	})
	if cycles := consumer.completedCycles(); cycles != 1 {
		t.Errorf("completed cycles = %d, want exactly 1 (only cycle 4 succeeded end to end)", cycles)
	}
	// Cycles 1, 2, 5 and 6 failed to collect; cycles 3 and 4 collected
	// successfully even though cycle 3's consumption failed.
	if collectFailures != 4 {
		t.Errorf("OnCollect reported %d collection failures, want 4", collectFailures)
	}
	if collectSuccesses != 2 {
		t.Errorf("OnCollect reported %d collection successes, want 2", collectSuccesses)
	}
}

// TestRun_BackoffCapsAtMaxInterval asserts that sustained failure settles at
// the configured maximum instead of growing without bound.
func TestRun_BackoffCapsAtMaxInterval(t *testing.T) {
	recorder := &baseRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backoff := BackoffConfig{
		InitialInterval:     util.Duration(time.Millisecond),
		MaxInterval:         util.Duration(4 * time.Millisecond),
		Multiplier:          2,
		RandomizationFactor: 0,
	}

	attempts := 0
	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		attempts++
		if attempts >= 6 {
			cancel()
		}
		return nil, errors.New("collection failed")
	}

	// With RandomizationFactor 0 the helper returns the base interval
	// unchanged and never consults the jitter source, so the capped sequence is
	// asserted directly on the arithmetic below.
	helper := NewHelper(
		"cap", time.Millisecond, backoff,
		internalLogger(), nil, recorder.jitter,
	)

	_ = helper.Run(ctx, collect, &scriptedConsumer{})

	// RandomizationFactor 0 short-circuits before the jitter source is called.
	if bases := recorder.snapshot(); len(bases) != 0 {
		t.Errorf("jitter source was consulted %d times with randomization disabled", len(bases))
	}

	// Assert the capped growth directly on the arithmetic.
	current := time.Duration(backoff.InitialInterval)
	want := []time.Duration{
		2 * time.Millisecond,
		4 * time.Millisecond,
		4 * time.Millisecond,
		4 * time.Millisecond,
	}
	for i, expected := range want {
		current = helper.advance(current)
		if current != expected {
			t.Fatalf("advance step %d = %s, want %s", i+1, current, expected)
		}
	}
}
