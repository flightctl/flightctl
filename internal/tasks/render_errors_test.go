package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/stretchr/testify/require"
)

func TestIsPermanentRenderError(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "When error is nil it should return false",
			err:      nil,
			expected: false,
		},
		{
			name:     "When error is ErrUnknownConfigName it should return true",
			err:      ErrUnknownConfigName,
			expected: true,
		},
		{
			name:     "When error wraps ErrUnknownConfigName it should return true",
			err:      fmt.Errorf("bad config: %w", ErrUnknownConfigName),
			expected: true,
		},
		{
			name:     "When error is ErrUnknownApplicationType it should return true",
			err:      ErrUnknownApplicationType,
			expected: true,
		},
		{
			name:     "When error wraps ErrUnknownApplicationType it should return true",
			err:      fmt.Errorf("bad app: %w", ErrUnknownApplicationType),
			expected: true,
		},
		{
			name:     "When error is ErrForbiddenDevicePath it should return true",
			err:      validation.ErrForbiddenDevicePath,
			expected: true,
		},
		{
			name:     "When error wraps ErrForbiddenDevicePath it should return true",
			err:      fmt.Errorf("invalid path from git config: %w", validation.ErrForbiddenDevicePath),
			expected: true,
		},
		{
			name:     "When error is a temporary DNS error it should return false",
			err:      &net.DNSError{Err: "temporary", IsTemporary: true},
			expected: false,
		},
		{
			name:     "When error is a non-temporary DNS error it should return false",
			err:      &net.DNSError{Err: "no such host", IsNotFound: true},
			expected: false,
		},
		{
			name:     "When error is ECONNRESET it should return false",
			err:      syscall.ECONNRESET,
			expected: false,
		},
		{
			name:     "When error is io.EOF it should return false",
			err:      io.EOF,
			expected: false,
		},
		{
			name:     "When error is context.DeadlineExceeded it should return false",
			err:      context.DeadlineExceeded,
			expected: false,
		},
		{
			name:     "When error is context.Canceled it should return false",
			err:      context.Canceled,
			expected: false,
		},
		{
			name:     "When error is a generic error it should return false",
			err:      errors.New("something went wrong"),
			expected: false,
		},
		{
			name:     "When error is a repo not found error it should return false",
			err:      fmt.Errorf("failed fetching specified Repository definition org/repo: not found"),
			expected: false,
		},
		{
			name:     "When error is HTTP unexpected status code it should return false",
			err:      fmt.Errorf("unexpected status code 503"),
			expected: false,
		},
		{
			name:     "When error is secret NotFound it should return false",
			err:      fmt.Errorf("failed getting secret default/my-secret: not found"),
			expected: false,
		},
		{
			name:     "When error is kubernetes API unavailable it should return false",
			err:      fmt.Errorf("kubernetes API is not available"),
			expected: false,
		},
		{
			name:     "When error wraps a network error it should return false",
			err:      fmt.Errorf("failed cloning git repo: %w", &net.DNSError{Err: "temporary", IsTemporary: true}),
			expected: false,
		},
		{
			name:     "When error wraps io.EOF it should return false",
			err:      fmt.Errorf("failed reading response: %w", io.EOF),
			expected: false,
		},
		{
			name:     "When error contains connection refused it should return false",
			err:      errors.New("dial tcp 10.0.0.1:443: connection refused"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPermanentRenderError(tt.err)
			require.Equal(tt.expected, result)
		})
	}
}
