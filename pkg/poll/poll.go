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
	ErrTimeout          = errors.New("operation timed out")
)

// Config defines parameters for exponential backoff polling.
type Config struct {
	// Initial delay before first retry
	BaseDelay time.Duration
	// Multiplier for delay on each retry
	Factor float64
	// Optional maximum delay between retries
	MaxDelay time.Duration
}

// BackoffWithContext repeatedly calls the operation until timeout is reached,
// it returns true, an error, or the context is canceled. It waits between
// attempts using exponential backoff, starting from Config.BaseDelay and
// increasing by Config.Factor, capped by Config.MaxDelay if set.
func BackoffWithContext(ctx context.Context, cfg *Config, timeout time.Duration, opFn func(context.Context) (bool, error)) error {
	if timeout <= 0 {
		return ErrInvalidTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	delay := cfg.BaseDelay
	if delay <= 0 {
		return fmt.Errorf("invalid Config: %w", ErrInvalidBaseDelay)
	}

	for {
		done, err := opFn(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
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

	delay := float64(cfg.BaseDelay)
	for i := 1; i < tries; i++ {
		delay *= cfg.Factor
	}

	delayDuration := time.Duration(delay)

	// cap max delay
	if cfg.MaxDelay > 0 && delayDuration > cfg.MaxDelay {
		delayDuration = cfg.MaxDelay
	}

	return delayDuration
}
