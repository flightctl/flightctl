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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.ctxTimeout)
			defer cancel()

			err := BackoffWithContext(ctx, tt.config, tt.operation())
			if tt.expectErr != nil {
				require.ErrorIs(err, tt.expectErr)
				return
			}
			require.NoError(err)
		})
	}
}
