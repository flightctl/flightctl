package poll

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackoffWithContext(t *testing.T) {
	require := require.New(t)
	opErr := errors.New("fatal op error")

	tests := []struct {
		name       string
		ctxTimeout time.Duration
		config     Config
		operation  func() func(context.Context) (bool, error)
		expectErr  error
	}{
		{
			name:       "immediate success",
			ctxTimeout: 1 * time.Second,
			config:     Config{BaseDelay: 10 * time.Millisecond, Factor: 2},
			operation: func() func(context.Context) (bool, error) {
				return func(context.Context) (bool, error) {
					return true, nil
				}
			},
			expectErr: nil,
		},
		{
			name:       "succeeds after retries",
			ctxTimeout: 500 * time.Millisecond,
			config:     Config{BaseDelay: 10 * time.Millisecond, Factor: 2},
			operation: func() func(context.Context) (bool, error) {
				attempts := 0
				return func(context.Context) (bool, error) {
					attempts++
					if attempts >= 3 {
						return true, nil
					}
					return false, nil
				}
			},
			expectErr: nil,
		},
		{
			name:       "fails with permanent error",
			ctxTimeout: 1 * time.Second,
			config:     Config{BaseDelay: 10 * time.Millisecond, Factor: 2},
			operation: func() func(context.Context) (bool, error) {
				return func(context.Context) (bool, error) {
					return false, opErr
				}
			},
			expectErr: opErr,
		},
		{
			name:       "context timeout cancels retries",
			ctxTimeout: 50 * time.Millisecond,
			config:     Config{BaseDelay: 30 * time.Millisecond, Factor: 2},
			operation: func() func(context.Context) (bool, error) {
				return func(context.Context) (bool, error) {
					return false, nil
				}
			},
			expectErr: context.DeadlineExceeded,
		},
		{
			name:       "invalid base delay",
			ctxTimeout: 50 * time.Millisecond,
			config:     Config{BaseDelay: 0, Factor: 2},
			operation: func() func(context.Context) (bool, error) {
				return func(context.Context) (bool, error) {
					return false, nil
				}
			},
			expectErr: ErrInvalidBaseDelay,
		},
		{
			name:       "respects ctx timeout",
			ctxTimeout: 500 * time.Millisecond,
			config:     Config{BaseDelay: 30 * time.Millisecond, Factor: 2},
			operation: func() func(context.Context) (bool, error) {
				return func(context.Context) (bool, error) {
					return false, nil
				}
			},
			expectErr: context.DeadlineExceeded,
		},
		{
			name:       "max steps exceeded",
			ctxTimeout: 5 * time.Second,
			config: Config{
				BaseDelay: 10 * time.Millisecond,
				Factor:    2,
				MaxSteps:  3,
			},
			operation: func() func(context.Context) (bool, error) {
				return func(context.Context) (bool, error) {
					// retryable
					return false, nil
				}
			},
			expectErr: ErrMaxSteps,
		},
		{
			name:       "invalid jitter factor - negative",
			ctxTimeout: 50 * time.Millisecond,
			config: Config{
				BaseDelay:    10 * time.Millisecond,
				Factor:       2,
				JitterFactor: -0.1,
			},
			operation: func() func(context.Context) (bool, error) {
				return func(context.Context) (bool, error) {
					return false, nil
				}
			},
			expectErr: errors.New("poll JitterFactor must be between 0.0 and 1.0"),
		},
		{
			name:       "invalid jitter factor - too high",
			ctxTimeout: 50 * time.Millisecond,
			config: Config{
				BaseDelay:    10 * time.Millisecond,
				Factor:       2,
				JitterFactor: 1.5,
			},
			operation: func() func(context.Context) (bool, error) {
				return func(context.Context) (bool, error) {
					return false, nil
				}
			},
			expectErr: errors.New("poll JitterFactor must be between 0.0 and 1.0"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.ctxTimeout)
			defer cancel()

			err := BackoffWithContext(ctx, tt.config, tt.operation())
			if tt.expectErr != nil {
				if tt.name == "invalid jitter factor - negative" || tt.name == "invalid jitter factor - too high" {
					require.ErrorContains(err, tt.expectErr.Error())
				} else {
					require.ErrorIs(err, tt.expectErr)
				}
				return
			}
			require.NoError(err)
		})
	}
}

func TestCalculateBackoffDelay(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name     string
		config   Config
		tries    int
		expected time.Duration
	}{
		{
			name: "no jitter",
			config: Config{
				BaseDelay: 10 * time.Millisecond,
				Factor:    2,
				MaxDelay:  100 * time.Millisecond,
			},
			tries:    3,
			expected: 40 * time.Millisecond, // 10 * 2^2 = 40ms
		},
		{
			name: "with jitter - should be within range",
			config: Config{
				BaseDelay:    10 * time.Millisecond,
				Factor:       2,
				MaxDelay:     100 * time.Millisecond,
				JitterFactor: 0.1, // 10% jitter
			},
			tries: 3,
			// Expected base: 40ms, jitter range: ±4ms, so result should be 36-44ms
			// We can't test exact value due to randomness, so we test range
		},
		{
			name: "zero tries",
			config: Config{
				BaseDelay: 10 * time.Millisecond,
				Factor:    2,
			},
			tries:    0,
			expected: 0,
		},
		{
			name: "negative tries",
			config: Config{
				BaseDelay: 10 * time.Millisecond,
				Factor:    2,
			},
			tries:    -1,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateBackoffDelay(&tt.config, tt.tries)

			if tt.name == "with jitter - should be within range" {
				// For jitter test, check that result is within expected range
				baseDelay := 40 * time.Millisecond // 10 * 2^2
				jitterRange := time.Duration(float64(baseDelay) * tt.config.JitterFactor)
				minDelay := baseDelay - jitterRange
				maxDelay := baseDelay + jitterRange

				require.GreaterOrEqual(result, minDelay, "jittered delay should be >= min")
				require.LessOrEqual(result, maxDelay, "jittered delay should be <= max")
			} else {
				require.Equal(tt.expected, result)
			}
		})
	}
}
