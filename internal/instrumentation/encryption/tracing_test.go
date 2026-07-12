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
			expected: "unsupported_strategy",
		},
		{
			name:     "ErrStrategyNotFound",
			err:      ErrStrategyNotFound,
			expected: "unsupported_strategy",
		},
		{
			name:     "ErrKeyNotFound",
			err:      ErrKeyNotFound,
			expected: "missing_key",
		},
		{
			name:     "ErrEncryptionFailed",
			err:      ErrEncryptionFailed,
			expected: "encrypt_failed",
		},
		{
			name:     "ErrDecryptionFailed",
			err:      ErrDecryptionFailed,
			expected: "decrypt_failed",
		},
		{
			name:     "ErrParseFailed",
			err:      ErrParseFailed,
			expected: "invalid_format",
		},
		{
			name:     "ErrInvalidFormat",
			err:      ErrInvalidFormat,
			expected: "invalid_format",
		},
		{
			name:     "ErrInvalidKey",
			err:      ErrInvalidKey,
			expected: "missing_key",
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
			result := CategorizeError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
