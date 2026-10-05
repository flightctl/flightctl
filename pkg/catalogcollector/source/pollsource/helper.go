// Package pollsource provides a reusable polling loop with bounded exponential
// backoff and jitter for pull-based catalog collector sources.
//
// A pull source supplies a collection callback and receives polling, backoff,
// cancellation, and collection-attempt notification behavior without coupling
// that behavior to the shared pipeline. Process readiness is not managed here;
// it is owned by the healthcheck extension.
package pollsource

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
)

// CollectFunc performs one complete collection cycle.
//
// A successful call returns one complete desired-state snapshot. An error must
// return no usable partial snapshot; the helper does not forward a snapshot
// when collection fails.
type CollectFunc func(
	ctx context.Context,
) (*catalogcollector.CatalogSnapshot, error)

// NowFunc returns the current time. It is used to measure collection-cycle
// duration and can be replaced by tests.
type NowFunc func() time.Time

// JitterFunc returns a random duration in [0, max). Tests may inject a
// deterministic implementation.
type JitterFunc func(max time.Duration) time.Duration

// BackoffConfig configures bounded exponential backoff with symmetric jitter.
type BackoffConfig struct {
	// InitialInterval is the base delay after the first failed cycle.
	InitialInterval util.Duration `json:"initialInterval,omitempty"`

	// MaxInterval is the maximum actual retry delay, including jitter.
	MaxInterval util.Duration `json:"maxInterval,omitempty"`

	// Multiplier advances the base interval after each consecutive failure.
	Multiplier float64 `json:"multiplier,omitempty"`

	// RandomizationFactor applies symmetric jitter around the base interval.
	// It must be within [0, 1].
	RandomizationFactor float64 `json:"randomizationFactor,omitempty"`
}

// Validate verifies that the backoff configuration can safely drive the
// polling loop.
func (c *BackoffConfig) Validate() error {
	initial := time.Duration(c.InitialInterval)
	maximum := time.Duration(c.MaxInterval)

	if initial <= 0 {
		return fmt.Errorf(
			"backoff.initialInterval must be positive, got %s",
			initial,
		)
	}
	if maximum <= 0 {
		return fmt.Errorf(
			"backoff.maxInterval must be positive, got %s",
			maximum,
		)
	}
	if initial > maximum {
		return fmt.Errorf(
			"backoff.initialInterval (%s) must not exceed "+
				"backoff.maxInterval (%s)",
			initial,
			maximum,
		)
	}

	if math.IsNaN(c.Multiplier) ||
		math.IsInf(c.Multiplier, 0) ||
		c.Multiplier < 1 {
		return fmt.Errorf(
			"backoff.multiplier must be finite and >= 1.0, got %g",
			c.Multiplier,
		)
	}

	if math.IsNaN(c.RandomizationFactor) ||
		math.IsInf(c.RandomizationFactor, 0) ||
		c.RandomizationFactor < 0 ||
		c.RandomizationFactor > 1 {
		return fmt.Errorf(
			"backoff.randomizationFactor must be finite and in [0, 1], got %g",
			c.RandomizationFactor,
		)
	}

	return nil
}

// DefaultBackoffConfig returns the default bounded-backoff configuration.
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		InitialInterval:     util.Duration(time.Second),
		MaxInterval:         util.Duration(5 * time.Minute),
		Multiplier:          2,
		RandomizationFactor: 0.5,
	}
}

// Helper drives a periodic complete-snapshot polling loop.
//
// The first cycle starts immediately. After collection and downstream
// consumption both succeed, the helper resets backoff and waits pollInterval.
// Any collection or downstream failure advances backoff.
//
// Collection cycles never overlap. Context cancellation interrupts collection,
// downstream processing when supported by the consumer, and inter-cycle waits.
type Helper struct {
	id           string
	pollInterval time.Duration
	backoff      BackoffConfig
	log          *logrus.Entry
	now          NowFunc
	jitter       JitterFunc

	// OnSuccess is called after collection and downstream consumption both
	// complete successfully.
	OnSuccess func(elapsed time.Duration)

	// OnFailure is called after collection or downstream consumption fails,
	// including cancellation of an in-progress cycle.
	OnFailure func(elapsed time.Duration, err error)
}

// NewHelper constructs a polling helper.
//
// now and jitter may be nil. Production clock and random-jitter implementations
// are used when they are not supplied.
func NewHelper(
	id string,
	pollInterval time.Duration,
	backoff BackoffConfig,
	log *logrus.Entry,
	now NowFunc,
	jitter JitterFunc,
) *Helper {
	if now == nil {
		now = time.Now
	}
	if jitter == nil {
		jitter = func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(max)))
			return time.Duration(n.Int64())
		}
	}

	return &Helper{
		id:           id,
		pollInterval: pollInterval,
		backoff:      backoff,
		log:          log,
		now:          now,
		jitter:       jitter,
	}
}

// Run executes polling cycles until the context is cancelled or invalid helper
// configuration is detected.
//
// Backoff resets only after both collection and downstream consumption succeed.
func (h *Helper) Run(
	ctx context.Context,
	collect CollectFunc,
	next catalogcollector.Consumer,
) error {
	if err := h.validate(collect, next); err != nil {
		return err
	}

	currentBackoff := time.Duration(h.backoff.InitialInterval)

	for {
		startedAt := h.now()

		snapshot, err := collect(ctx)
		if ctxErr := ctx.Err(); ctxErr != nil {
			h.notifyFailure(h.elapsedSince(startedAt), ctxErr)
			return ctxErr
		}
		if err != nil {
			currentBackoff = h.handleRetryableFailure(
				ctx,
				startedAt,
				currentBackoff,
				err,
				"collection failed; applying backoff",
			)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if snapshot == nil {
			err = fmt.Errorf(
				"polling source %q returned a nil snapshot without an error",
				h.id,
			)
			currentBackoff = h.handleRetryableFailure(
				ctx,
				startedAt,
				currentBackoff,
				err,
				"collection failed; applying backoff",
			)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		err = next.Consume(ctx, snapshot)
		if ctxErr := ctx.Err(); ctxErr != nil {
			h.notifyFailure(h.elapsedSince(startedAt), ctxErr)
			return ctxErr
		}
		if err != nil {
			currentBackoff = h.handleRetryableFailure(
				ctx,
				startedAt,
				currentBackoff,
				err,
				"downstream consumption failed; applying backoff",
			)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		elapsed := h.elapsedSince(startedAt)
		if h.OnSuccess != nil {
			h.OnSuccess(elapsed)
		}

		currentBackoff = time.Duration(h.backoff.InitialInterval)
		if !h.wait(ctx, h.pollInterval) {
			return ctx.Err()
		}
	}
}

func (h *Helper) validate(
	collect CollectFunc,
	next catalogcollector.Consumer,
) error {
	if h.pollInterval <= 0 {
		return fmt.Errorf(
			"polling source %q: poll interval must be positive, got %s",
			h.id,
			h.pollInterval,
		)
	}
	if err := h.backoff.Validate(); err != nil {
		return fmt.Errorf("polling source %q: %w", h.id, err)
	}
	if h.log == nil {
		return fmt.Errorf("polling source %q: logger must not be nil", h.id)
	}
	if collect == nil {
		return fmt.Errorf(
			"polling source %q: collection callback must not be nil",
			h.id,
		)
	}
	if next == nil {
		return fmt.Errorf(
			"polling source %q: downstream consumer must not be nil",
			h.id,
		)
	}

	return nil
}

// handleRetryableFailure records and logs one failed cycle, waits for the
// current jittered backoff, and returns the advanced base interval.
//
// If the context is cancelled during the wait, Run observes ctx.Err() and
// exits rather than starting another cycle.
func (h *Helper) handleRetryableFailure(
	ctx context.Context,
	startedAt time.Time,
	currentBackoff time.Duration,
	err error,
	message string,
) time.Duration {
	elapsed := h.elapsedSince(startedAt)
	h.notifyFailure(elapsed, err)

	retryAfter := h.withJitter(currentBackoff)
	h.log.WithError(err).
		WithField("retry_after", retryAfter).
		Error(message)

	nextBackoff := h.advance(currentBackoff)
	_ = h.wait(ctx, retryAfter)

	return nextBackoff
}

func (h *Helper) notifyFailure(
	elapsed time.Duration,
	err error,
) {
	if h.OnFailure != nil {
		h.OnFailure(elapsed, err)
	}
}

func (h *Helper) elapsedSince(start time.Time) time.Duration {
	elapsed := h.now().Sub(start)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// withJitter applies symmetric jitter while keeping the actual retry delay
// positive and no greater than MaxInterval.
func (h *Helper) withJitter(base time.Duration) time.Duration {
	maximum := time.Duration(h.backoff.MaxInterval)
	if base > maximum {
		base = maximum
	}

	factor := h.backoff.RandomizationFactor
	if factor == 0 {
		return base
	}

	delta := time.Duration(float64(base) * factor)
	if delta < 0 || delta > base {
		delta = base
	}

	lower := base - delta
	upper := base + delta

	if upper < base || upper > maximum {
		upper = maximum
	}
	if lower < time.Nanosecond {
		lower = time.Nanosecond
	}
	if upper <= lower {
		return lower
	}

	width := upper - lower
	offset := h.jitter(width)
	if offset < 0 {
		offset = 0
	}
	if offset >= width {
		offset = width - 1
	}

	return lower + offset
}

// advance multiplies the current base interval and caps it at MaxInterval.
func (h *Helper) advance(current time.Duration) time.Duration {
	maximum := time.Duration(h.backoff.MaxInterval)
	if current >= maximum {
		return maximum
	}

	// Check before multiplication to avoid duration overflow.
	if float64(current) >= float64(maximum)/h.backoff.Multiplier {
		return maximum
	}

	next := time.Duration(float64(current) * h.backoff.Multiplier)

	// When multiplication produces no increase (e.g. multiplier == 1),
	// preserve the current interval instead of jumping to maximum.
	if next == current {
		return current
	}

	// Guard against overflow: if multiplication wrapped around and produced
	// a smaller value, or exceeded the cap, clamp to maximum.
	if next < current || next > maximum {
		return maximum
	}

	return next
}

// wait blocks until the duration elapses or the context is cancelled.
func (h *Helper) wait(
	ctx context.Context,
	duration time.Duration,
) bool {
	if duration <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
