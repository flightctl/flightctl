package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsRetryableRenderError(t *testing.T) {
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
			name:     "When error is a temporary DNS error it should return true",
			err:      &net.DNSError{Err: "temporary", IsTemporary: true},
			expected: true,
		},
		{
			name:     "When error is a non-temporary DNS error it should return false",
			err:      &net.DNSError{Err: "no such host", IsNotFound: true},
			expected: false,
		},
		{
			name:     "When error is a timeout net.Error it should return true",
			err:      &timeoutNetError{},
			expected: true,
		},
		{
			name:     "When error is ECONNRESET it should return true",
			err:      syscall.ECONNRESET,
			expected: true,
		},
		{
			name:     "When error is ETIMEDOUT it should return true",
			err:      syscall.ETIMEDOUT,
			expected: true,
		},
		{
			name:     "When error is io.EOF it should return true",
			err:      io.EOF,
			expected: true,
		},
		{
			name:     "When error is io.ErrUnexpectedEOF it should return true",
			err:      io.ErrUnexpectedEOF,
			expected: true,
		},
		{
			name:     "When error is context.DeadlineExceeded it should return true",
			err:      context.DeadlineExceeded,
			expected: true,
		},
		{
			name:     "When error is context.Canceled it should return true",
			err:      context.Canceled,
			expected: true,
		},
		{
			name:     "When error wraps a transient error it should return true",
			err:      fmt.Errorf("failed cloning git repo: %w", &net.DNSError{Err: "temporary", IsTemporary: true}),
			expected: true,
		},
		{
			name:     "When error wraps ECONNRESET it should return true",
			err:      fmt.Errorf("failed fetching data: %w", syscall.ECONNRESET),
			expected: true,
		},
		{
			name:     "When error wraps io.EOF it should return true",
			err:      fmt.Errorf("failed reading response: %w", io.EOF),
			expected: true,
		},
		{
			name:     "When error contains 'connection refused' it should return true",
			err:      errors.New("dial tcp 10.0.0.1:443: connection refused"),
			expected: true,
		},
		{
			name:     "When error contains 'connection reset' it should return true",
			err:      errors.New("read tcp: connection reset by peer"),
			expected: true,
		},
		{
			name:     "When error contains 'i/o timeout' it should return true",
			err:      errors.New("dial tcp: i/o timeout"),
			expected: true,
		},
		{
			name:     "When error contains 'unexpected EOF' it should return true",
			err:      errors.New("http: unexpected EOF reading trailer"),
			expected: true,
		},
		{
			name:     "When error is a config validation error it should return false",
			err:      fmt.Errorf("invalid path from git config: %w", errors.New("forbidden device path")),
			expected: false,
		},
		{
			name:     "When error is ErrUnknownConfigName it should return false",
			err:      fmt.Errorf("bad config: %w", ErrUnknownConfigName),
			expected: false,
		},
		{
			name:     "When error is ErrUnknownApplicationType it should return false",
			err:      fmt.Errorf("bad app: %w", ErrUnknownApplicationType),
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRetryableRenderError(tt.err)
			require.Equal(tt.expected, result)
		})
	}
}

type timeoutNetError struct{}

func (e *timeoutNetError) Error() string   { return "timeout" }
func (e *timeoutNetError) Timeout() bool   { return true }
func (e *timeoutNetError) Temporary() bool { return true }
