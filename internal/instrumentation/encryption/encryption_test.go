package encryption

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCategorizeError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "When error is ErrNoActiveStrategy it should return unsupported_strategy",
			err:      ErrNoActiveStrategy,
			expected: "unsupported_strategy",
		},
		{
			name:     "When error is ErrStrategyNotFound it should return unsupported_strategy",
			err:      ErrStrategyNotFound,
			expected: "unsupported_strategy",
		},
		{
			name:     "When error is ErrKeyNotFound it should return missing_key",
			err:      ErrKeyNotFound,
			expected: "missing_key",
		},
		{
			name:     "When error is ErrEncryptionFailed it should return encrypt_failed",
			err:      ErrEncryptionFailed,
			expected: "encrypt_failed",
		},
		{
			name:     "When error is ErrDecryptionFailed it should return decrypt_failed",
			err:      ErrDecryptionFailed,
			expected: "decrypt_failed",
		},
		{
			name:     "When error is ErrParseFailed it should return invalid_format",
			err:      ErrParseFailed,
			expected: "invalid_format",
		},
		{
			name:     "When error is ErrInvalidFormat it should return invalid_format",
			err:      ErrInvalidFormat,
			expected: "invalid_format",
		},
		{
			name:     "When error is ErrInvalidKey it should return missing_key",
			err:      ErrInvalidKey,
			expected: "missing_key",
		},
		{
			name:     "When error is ErrCanaryMismatch it should return canary_mismatch",
			err:      ErrCanaryMismatch,
			expected: "canary_mismatch",
		},
		{
			name:     "When error wraps ErrDecryptionFailed it should return decrypt_failed",
			err:      fmt.Errorf("%w: %v", ErrDecryptionFailed, errors.New("underlying crypto error")),
			expected: "decrypt_failed",
		},
		{
			name:     "When error wraps ErrEncryptionFailed it should return encrypt_failed",
			err:      fmt.Errorf("%w: %v", ErrEncryptionFailed, errors.New("key derivation failed")),
			expected: "encrypt_failed",
		},
		{
			name:     "When error wraps ErrKeyNotFound it should return missing_key",
			err:      fmt.Errorf("%w: key xyz not found", ErrKeyNotFound),
			expected: "missing_key",
		},
		{
			name:     "When error wraps ErrParseFailed it should return invalid_format",
			err:      fmt.Errorf("%w: %v", ErrParseFailed, errors.New("invalid base64")),
			expected: "invalid_format",
		},
		{
			name:     "When error is unknown it should return operation_failed",
			err:      errors.New("some random error"),
			expected: "operation_failed",
		},
		{
			name:     "When error is nil it should return empty string",
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
