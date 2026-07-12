package encryption

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTracing_ErrorCategorization(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "ErrNoActiveStrategy",
			err:      ErrNoActiveStrategy,
			expected: "no_active_strategy",
		},
		{
			name:     "ErrStrategyNotFound",
			err:      ErrStrategyNotFound,
			expected: "strategy_not_found",
		},
		{
			name:     "ErrKeyNotFound",
			err:      ErrKeyNotFound,
			expected: "key_not_found",
		},
		{
			name:     "ErrEncryptionFailed",
			err:      ErrEncryptionFailed,
			expected: "encryption_failed",
		},
		{
			name:     "ErrDecryptionFailed",
			err:      ErrDecryptionFailed,
			expected: "decryption_failed",
		},
		{
			name:     "ErrParseFailed",
			err:      ErrParseFailed,
			expected: "parse_failed",
		},
		{
			name:     "ErrInvalidFormat",
			err:      ErrInvalidFormat,
			expected: "invalid_format",
		},
		{
			name:     "ErrInvalidKey",
			err:      ErrInvalidKey,
			expected: "invalid_key",
		},
		{
			name:     "Unknown error",
			err:      errors.New("some random error"),
			expected: "operation_failed",
		},
		{
			name:     "Nil error",
			err:      nil,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := categorizeError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
