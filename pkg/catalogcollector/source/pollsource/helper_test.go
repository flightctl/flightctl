package pollsource_test

import (
	"context"
	"errors"
	"strings"
	"sync"
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
//
// A completed cycle is observed here rather than through a helper callback:
// the helper reports collection outcomes only, so successful consumption is
// the signal that both halves of a cycle finished.
type fakeConsumer struct {
	calls     atomic.Int64
	failN     int // fail the first N calls
	failed    atomic.Int64
	succeeded atomic.Int64

	// onSuccess, when set, runs after each successful consumption.
	onSuccess func()
}

func (c *fakeConsumer) Consume(_ context.Context, _ *catalogcollector.CatalogSnapshot) error {
	n := int(c.calls.Add(1))
	if c.failN > 0 && n <= c.failN {
		c.failed.Add(1)
		return errors.New("consumer error")
	}

	c.succeeded.Add(1)
	if c.onSuccess != nil {
		c.onSuccess()
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
	h.OnCollect = func(_ time.Duration, err error) {
		if err != nil {
			failures.Add(1)
		}
	}

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
		t.Error("expected OnCollect to report a collection failure")
	}
	if n := consumer.calls.Load(); n != 0 {
		t.Errorf("consumer was called %d times; a failed collection must never reach the consumer", n)
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

	b := pollsource.BackoffConfig{
		InitialInterval:     ud(10 * time.Millisecond),
		MaxInterval:         ud(200 * time.Millisecond),
		Multiplier:          2.0,
		RandomizationFactor: 0,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// A completed cycle is observed at the consumer, which runs only after a
	// successful collection.
	consumer := &fakeConsumer{}
	consumer.onSuccess = cancel // stop after the first completed cycle

	h := pollsource.NewHelper("test", time.Hour, b, newLogger(), nil, noJitter)

	done := make(chan error, 1)
	go func() { done <- h.Run(ctx, collect, consumer) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("test timed out")
	}

	if n := consumer.succeeded.Load(); n != 1 {
		t.Errorf("completed cycles = %d, want exactly 1", n)
	}
	if n := callCount.Load(); n < 3 {
		t.Errorf("expected at least 3 calls (2 failures + 1 success), got %d", n)
	}
}

func TestHelper_DownstreamConsumeFailureAppliesBackoff(t *testing.T) {
	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return emptySnapshot, nil
	}

	consumer := &fakeConsumer{failN: 2} // first 2 consumes fail

	b := pollsource.BackoffConfig{
		InitialInterval:     ud(10 * time.Millisecond),
		MaxInterval:         ud(100 * time.Millisecond),
		Multiplier:          2.0,
		RandomizationFactor: 0,
	}

	ctx, cancel := context.WithCancel(context.Background())
	consumer.onSuccess = cancel // stop once a cycle completes end to end

	h := pollsource.NewHelper("test", time.Hour, b, newLogger(), nil, noJitter)

	// Every collection succeeds, so the collection outcome must stay
	// successful even while the downstream keeps rejecting the snapshot.
	var collectFailures atomic.Int64
	h.OnCollect = func(_ time.Duration, err error) {
		if err != nil {
			collectFailures.Add(1)
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
	if n := collectFailures.Load(); n != 0 {
		t.Errorf("OnCollect reported %d collection failures; downstream failures must not be attributed to collection", n)
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
	h.OnCollect = func(_ time.Duration, err error) {
		if err != nil {
			failCount.Add(1)
		}
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

	ctx, cancel := context.WithCancel(context.Background())

	consumer := &fakeConsumer{}
	consumer.onSuccess = func() {
		if consumer.succeeded.Load() >= 2 {
			cancel() // stop after the second completed cycle
		}
	}

	h := pollsource.NewHelper("test-reset", 5*time.Millisecond, b, newLogger(), nil, noJitter)

	done := make(chan error, 1)
	go func() { done <- h.Run(ctx, collect, consumer) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("test timed out")
	}

	// We need at least 6 calls: fail, fail, success, fail, fail, success
	if n := callCount.Load(); n < 6 {
		t.Errorf("expected at least 6 calls (2 cycles of fail-fail-succeed), got %d", n)
	}
	if n := consumer.succeeded.Load(); n < 2 {
		t.Errorf("expected at least 2 completed cycles, got %d", n)
	}
}

// --- OnCollect -------------------------------------------------------------

// collectObservation captures one OnCollect invocation.
type collectObservation struct {
	elapsed time.Duration
	err     error
}

// cycleRecorder records OnCollect invocations and consumer calls in the order
// they happen, so the relative ordering of collection reporting and downstream
// consumption can be asserted directly.
type cycleRecorder struct {
	mu           sync.Mutex
	order        []string
	observations []collectObservation
}

func (r *cycleRecorder) onCollect(elapsed time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, "collect")
	r.observations = append(r.observations, collectObservation{elapsed: elapsed, err: err})
}

func (r *cycleRecorder) onConsume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, "consume")
}

func (r *cycleRecorder) snapshot() ([]string, []collectObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...),
		append([]collectObservation(nil), r.observations...)
}

// recordingConsumer reports every consumption to a cycleRecorder and returns a
// configurable error.
type recordingConsumer struct {
	recorder *cycleRecorder
	err      error
	onCall   func()
}

func (c *recordingConsumer) Consume(
	_ context.Context,
	_ *catalogcollector.CatalogSnapshot,
) error {
	c.recorder.onConsume()
	if c.onCall != nil {
		c.onCall()
	}
	return c.err
}

// fakeClock is a manually advanced clock injected as the helper's NowFunc.
type fakeClock struct {
	mu      sync.Mutex
	current time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

// TestHelper_OnCollect_SuccessReportedBeforeConsumption asserts that a
// successful collection is reported exactly once, with no error, and before
// the snapshot reaches the downstream consumer.
func TestHelper_OnCollect_SuccessReportedBeforeConsumption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &cycleRecorder{}
	consumer := &recordingConsumer{recorder: recorder, onCall: cancel}

	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return emptySnapshot, nil
	}

	h := pollsource.NewHelper("on-collect", time.Hour, defaultBackoff(), newLogger(), nil, noJitter)
	h.OnCollect = recorder.onCollect

	_ = h.Run(ctx, collect, consumer)

	order, observations := recorder.snapshot()

	if len(observations) != 1 {
		t.Fatalf("OnCollect fired %d times, want exactly 1", len(observations))
	}
	if observations[0].err != nil {
		t.Errorf("OnCollect error = %v, want nil for a successful collection", observations[0].err)
	}
	if len(order) != 2 || order[0] != "collect" || order[1] != "consume" {
		t.Errorf("event order = %v, want [collect consume]", order)
	}
}

// TestHelper_OnCollect_CollectionFailurePreservesError asserts that a failed
// collection is reported once with the original error value, and that nothing
// reaches the downstream consumer.
func TestHelper_OnCollect_CollectionFailurePreservesError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	collectErr := errors.New("registry unreachable")
	recorder := &cycleRecorder{}
	consumer := &recordingConsumer{recorder: recorder}

	var attempts atomic.Int64
	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		attempts.Add(1)
		return nil, collectErr
	}

	h := pollsource.NewHelper("on-collect-fail", time.Hour, defaultBackoff(), newLogger(), nil, noJitter)
	h.OnCollect = func(elapsed time.Duration, err error) {
		recorder.onCollect(elapsed, err)
		cancel() // one observed attempt is enough
	}

	_ = h.Run(ctx, collect, consumer)

	order, observations := recorder.snapshot()

	if len(observations) != 1 {
		t.Fatalf("OnCollect fired %d times, want exactly 1", len(observations))
	}
	if !errors.Is(observations[0].err, collectErr) {
		t.Errorf("OnCollect error = %v, want the original collection error %v", observations[0].err, collectErr)
	}
	// Identity, not just wrapping: the helper must report the collector's own
	// error value rather than a substituted or re-wrapped one.
	if observations[0].err != collectErr {
		t.Errorf("OnCollect error identity changed: got %#v, want %#v", observations[0].err, collectErr)
	}
	for _, event := range order {
		if event == "consume" {
			t.Fatal("a failed collection reached the downstream consumer")
		}
	}
}

// TestHelper_OnCollect_NilSnapshotWithoutErrorReportsFailure asserts that a
// collector returning neither a snapshot nor an error is converted into a
// collection failure before the callback runs, and never reaches the consumer.
func TestHelper_OnCollect_NilSnapshotWithoutErrorReportsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &cycleRecorder{}
	consumer := &recordingConsumer{recorder: recorder}

	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return nil, nil
	}

	h := pollsource.NewHelper("nil-snap", time.Hour, defaultBackoff(), newLogger(), nil, noJitter)
	h.OnCollect = func(elapsed time.Duration, err error) {
		recorder.onCollect(elapsed, err)
		cancel()
	}

	_ = h.Run(ctx, collect, consumer)

	order, observations := recorder.snapshot()

	if len(observations) != 1 {
		t.Fatalf("OnCollect fired %d times, want exactly 1", len(observations))
	}
	if observations[0].err == nil {
		t.Fatal("OnCollect error = nil, want a collection failure for a nil snapshot")
	}
	if !strings.Contains(observations[0].err.Error(), "nil snapshot") {
		t.Errorf("OnCollect error = %q, want it to name the nil snapshot", observations[0].err)
	}
	for _, event := range order {
		if event == "consume" {
			t.Fatal("a nil snapshot reached the downstream consumer")
		}
	}
}

// TestHelper_OnCollect_DownstreamFailureKeepsCollectionSuccessful asserts that
// a failing consumer never turns a successful collection into a collection
// failure, while still driving backoff and failing the cycle.
func TestHelper_OnCollect_DownstreamFailureKeepsCollectionSuccessful(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &cycleRecorder{}
	consumeErr := errors.New("downstream rejected the snapshot")

	var consumed atomic.Int64
	consumer := &recordingConsumer{
		recorder: recorder,
		err:      consumeErr,
		onCall: func() {
			if consumed.Add(1) >= 2 {
				cancel() // two failed cycles prove the retry
			}
		},
	}

	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return emptySnapshot, nil
	}

	h := pollsource.NewHelper("downstream-fail", time.Hour, defaultBackoff(), newLogger(), nil, noJitter)
	h.OnCollect = recorder.onCollect

	err := h.Run(ctx, collect, consumer)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run() = %v, want context.Canceled", err)
	}

	_, observations := recorder.snapshot()

	if len(observations) < 2 {
		t.Fatalf("OnCollect fired %d times, want at least 2", len(observations))
	}
	for i, observation := range observations {
		if observation.err != nil {
			t.Errorf("OnCollect[%d] error = %v, want nil; downstream failures must not be attributed to collection", i, observation.err)
		}
	}
	if n := consumed.Load(); n < 2 {
		t.Errorf("consumer called %d times, want at least 2 (the downstream failure must trigger a retry)", n)
	}
}

// TestHelper_OnCollect_DurationExcludesConsumer asserts, using the injected
// clock, that the reported duration covers collection alone.
func TestHelper_OnCollect_DurationExcludesConsumer(t *testing.T) {
	const (
		collectCost = 5 * time.Millisecond
		consumeCost = 500 * time.Millisecond
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := newFakeClock()
	recorder := &cycleRecorder{}

	consumer := &recordingConsumer{
		recorder: recorder,
		onCall: func() {
			clock.advance(consumeCost)
			cancel()
		},
	}

	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		clock.advance(collectCost)
		return emptySnapshot, nil
	}

	h := pollsource.NewHelper(
		"duration", time.Hour, defaultBackoff(), newLogger(), clock.now, noJitter,
	)
	h.OnCollect = recorder.onCollect

	_ = h.Run(ctx, collect, consumer)

	_, observations := recorder.snapshot()

	if len(observations) != 1 {
		t.Fatalf("OnCollect fired %d times, want exactly 1", len(observations))
	}
	if observations[0].elapsed != collectCost {
		t.Errorf(
			"OnCollect elapsed = %s, want %s (the consumer's %s must be excluded)",
			observations[0].elapsed, collectCost, consumeCost,
		)
	}
}

// TestHelper_OnCollect_CancellationDuringCollection asserts that a collection
// interrupted by cancellation is reported once, carrying the cancellation
// error, and that no snapshot reaches the consumer.
func TestHelper_OnCollect_CancellationDuringCollection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &cycleRecorder{}
	consumer := &recordingConsumer{recorder: recorder}

	collect := func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}

	h := pollsource.NewHelper("cancel-collect", time.Hour, defaultBackoff(), newLogger(), nil, noJitter)
	h.OnCollect = recorder.onCollect

	err := h.Run(ctx, collect, consumer)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run() = %v, want context.Canceled", err)
	}

	order, observations := recorder.snapshot()

	if len(observations) != 1 {
		t.Fatalf("OnCollect fired %d times, want exactly 1", len(observations))
	}
	if !errors.Is(observations[0].err, context.Canceled) {
		t.Errorf("OnCollect error = %v, want context.Canceled", observations[0].err)
	}
	for _, event := range order {
		if event == "consume" {
			t.Fatal("a cancelled collection reached the downstream consumer")
		}
	}
}

// TestHelper_OnCollect_CancellationDuringConsumptionKeepsCollectionSuccessful
// asserts that cancellation observed while the consumer runs does not rewrite
// the collection outcome that was already reported.
func TestHelper_OnCollect_CancellationDuringConsumptionKeepsCollectionSuccessful(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &cycleRecorder{}
	consumer := &recordingConsumer{
		recorder: recorder,
		onCall:   cancel, // cancel while consuming
	}

	collect := func(context.Context) (*catalogcollector.CatalogSnapshot, error) {
		return emptySnapshot, nil
	}

	h := pollsource.NewHelper("cancel-consume", time.Hour, defaultBackoff(), newLogger(), nil, noJitter)
	h.OnCollect = recorder.onCollect

	err := h.Run(ctx, collect, consumer)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run() = %v, want context.Canceled", err)
	}

	order, observations := recorder.snapshot()

	if len(observations) != 1 {
		t.Fatalf("OnCollect fired %d times, want exactly 1", len(observations))
	}
	if observations[0].err != nil {
		t.Errorf(
			"OnCollect error = %v, want nil; cancellation during consumption must not change the collection outcome",
			observations[0].err,
		)
	}
	if len(order) != 2 || order[0] != "collect" || order[1] != "consume" {
		t.Errorf("event order = %v, want [collect consume]", order)
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
