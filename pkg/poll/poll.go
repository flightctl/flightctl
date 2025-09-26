package poll

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

var (
	ErrInvalidBaseDelay = errors.New("BaseDelay must be greater than 0")
	ErrInvalidTimeout   = errors.New("timeout must be greater than 0")
	ErrMaxSteps         = errors.New("max poll retry steps exceeded")
)

// Config defines parameters for exponential backoff polling.
type Config struct {
	// Initial delay before first retry
	BaseDelay time.Duration
	// Multiplier for delay on each retry
	Factor float64
	// Optional maximum delay between retries
	MaxDelay time.Duration
	// MaxSteps limits the number of retries. If 0, retries will continue until timeout.
	MaxSteps int
	// JitterFactor adds randomization to prevent thundering herd (0.0 to 1.0)
	JitterFactor float64
	// Rand is the random number generator for jitter calculation
	Rand *rand.Rand
}

// NewConfig creates a new Config with a properly seeded random number generator.
func NewConfig(baseDelay time.Duration, factor float64) *Config {
	return &Config{
		BaseDelay:    baseDelay,
		Factor:       factor,
		JitterFactor: 0.1,                                             // Default jitter factor
		Rand:         rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec
	}
}

// Validate checks if the configuration parameters are valid
func (c *Config) Validate() error {
	if c.BaseDelay <= 0 {
		return ErrInvalidBaseDelay
	}
	if c.Factor <= 0 {
		return errors.New("poll Factor must be greater than 0")
	}
	if c.MaxDelay > 0 && c.MaxDelay < c.BaseDelay {
		return errors.New("poll MaxDelay must be greater than or equal to BaseDelay")
	}
	if c.JitterFactor < 0 || c.JitterFactor > 1 {
		return errors.New("poll JitterFactor must be between 0.0 and 1.0")
	}
	if c.Rand == nil {
		return errors.New("poll Rand must not be nil")
	}
	return nil
}

// calculateJitter calculates jitter for a given delay based on the jitter factor.
func calculateJitter(rng *rand.Rand, delay time.Duration, jitterFactor float64) time.Duration {
	if jitterFactor <= 0 {
		return 0
	}
	jitterRange := float64(delay) * jitterFactor
	return time.Duration((rng.Float64()*2 - 1) * jitterRange)
}

// BackoffWithContext repeatedly calls the operation until timeout is reached,
// it returns true, an error, or the context is canceled. It waits between
// attempts using exponential backoff, starting from Config.BaseDelay and
// increasing by Config.Factor, capped by Config.MaxDelay if set.
func BackoffWithContext(ctx context.Context, cfg Config, opFn func(context.Context) (bool, error)) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	delay := cfg.BaseDelay
	attempts := 0
	for {

		done, err := opFn(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		attempts++
		if cfg.MaxSteps > 0 && attempts >= cfg.MaxSteps {
			return fmt.Errorf("%w: %d", ErrMaxSteps, cfg.MaxSteps)
		}

		select {
		case <-time.After(delay):
			next := time.Duration(float64(delay) * cfg.Factor)
			if cfg.MaxDelay > 0 && next > cfg.MaxDelay {
				next = cfg.MaxDelay
			}

			// Add jitter calculated per retry attempt to prevent thundering herd
			jitter := calculateJitter(cfg.Rand, next, cfg.JitterFactor)
			next += jitter

			// Ensure delay doesn't go negative
			if next < 0 {
				next = 0
			}

			delay = next
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// CalculateBackoffDelay calculates the backoff delay for a given number of tries
// using exponential backoff with the provided configuration.
func CalculateBackoffDelay(cfg *Config, tries int) time.Duration {
	if tries <= 0 {
		return 0
	}

	// maxDurationFloat is the largest valid time.Duration value as a float64.
	// used to prevent overflow when computing exponential delays.
	const maxDurationFloat = float64(time.Duration(1<<63 - 1))

	delay := float64(cfg.BaseDelay)
	for i := 1; i < tries; i++ {
		next := delay * cfg.Factor
		if next > maxDurationFloat {
			delay = maxDurationFloat
			break
		}
		delay = next
	}

	delayDuration := time.Duration(delay)

	// cap max delay
	if cfg.MaxDelay > 0 && delayDuration > cfg.MaxDelay {
		delayDuration = cfg.MaxDelay
	}

	// Add jitter calculated per attempt to prevent thundering herd
	jitter := calculateJitter(cfg.Rand, delayDuration, cfg.JitterFactor)
	delayDuration += jitter

	// Ensure delay doesn't go negative
	if delayDuration < 0 {
		delayDuration = 0
	}

	return delayDuration
}
