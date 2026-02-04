package errors

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFromStderr(t *testing.T) {
	testCases := []struct {
		name       string
		stderr     string
		exitCode   int
		expected   error
		shouldWrap bool
	}{
		{
			name:       "unauthorized returns ErrImageUnauthorized",
			stderr:     "unauthorized: access to the requested resource is not authorized",
			exitCode:   125,
			expected:   ErrImageUnauthorized,
			shouldWrap: true,
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
			require := require.New(t)
			err := FromStderr(tc.stderr, tc.exitCode)

			if tc.shouldWrap {
				require.ErrorIs(err, tc.expected)
			} else {
				require.Equal(tc.expected, err)
			}
		})
	}
}
