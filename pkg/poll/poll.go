package poll

import (
	"context"
	"errors"
	"fmt"
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
	return nil
}

// BackoffWithContext repeatedly calls the operation until timeout is reached,
// it returns true, an error, or the context is canceled. It waits between
// attempts using exponential backoff, starting from Config.BaseDelay and
// increasing by Config.Factor, capped by Config.MaxDelay if set.
func BackoffWithContext(ctx context.Context, cfg Config, timeout time.Duration, opFn func(context.Context) (bool, error)) error {
	if timeout <= 0 {
		return ErrInvalidTimeout
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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

	return delayDuration
}
