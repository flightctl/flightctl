package errors

import (
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
)

func TestFromStderr(t *testing.T) {
	testCases := []struct {
		name       string
		stderr     string
		exitCode   int
		expected   error
		shouldWrap bool
		retryable  bool
	}{
		{
			name:       "unauthorized returns ErrImageUnauthorized",
			stderr:     "unauthorized: access to the requested resource is not authorized",
			exitCode:   125,
			expected:   ErrImageUnauthorized,
			shouldWrap: true,
		},
		{
			name:       "When the manifest is unknown it should retry ErrImageNotFound",
			stderr:     "Error: manifest unknown",
			exitCode:   1,
			expected:   ErrImageNotFound,
			shouldWrap: true,
			retryable:  true,
		},
		{
			name:       "authentication required returns ErrAuthenticationFailed",
			stderr:     "authentication required",
			exitCode:   125,
			expected:   ErrAuthenticationFailed,
			shouldWrap: true,
		},
		{
			name:       "not found returns ErrNotFound",
			stderr:     "not found",
			exitCode:   1,
			expected:   ErrNotFound,
			shouldWrap: true,
		},
		{
			name:     "unknown error returns generic error",
			stderr:   "some other error",
			exitCode: 1,
			expected: fmt.Errorf("code: 1: some other error"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("testing stderr: %q, exit code: %d", tc.stderr, tc.exitCode)
			require := require.New(t)
			err := FromStderr(tc.stderr, tc.exitCode)

			if tc.shouldWrap {
				require.ErrorIs(err, tc.expected)
			} else {
				require.Equal(tc.expected, err)
			}
			require.Equal(tc.retryable, IsRetryable(err))
		})
	}
}

// TestIsRetryableNoRetryMarkerTakesPrecedence verifies that terminal errors
// remain non-retryable when they wrap a retryable cause.
func TestIsRetryableNoRetryMarkerTakesPrecedence(t *testing.T) {
	err := fmt.Errorf("%w: %w", ErrNoRetry, poll.ErrMaxSteps)

	require.False(t, IsRetryable(err))
}
