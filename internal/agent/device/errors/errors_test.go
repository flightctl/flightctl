package errors

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFromStderr(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name          string
		stderr        string
		exitCode      int
		expectedErr   error
		expectedMsg   string
		expectedIs    error
		expectedIsNil bool
	}{
		{
			name:        "image not found disguised as unauthorized",
			stderr:      "Error: initializing source docker://quay.io/kenosborn/does-not-exist:v1: reading manifest v1 in quay.io/kenosborn/does-not-exist: unauthorized: access to the requested resource is not authorized",
			exitCode:    125,
			expectedErr: &stderrError{wrapped: ErrImageNotFound, reason: "manifest unknown", code: 125, stderr: "Error: initializing source docker://quay.io/kenosborn/does-not-exist:v1: reading manifest v1 in quay.io/kenosborn/does-not-exist: unauthorized: access to the requested resource is not authorized"},
			expectedMsg: "image not found: code: 125: Error: initializing source docker://quay.io/kenosborn/does-not-exist:v1: reading manifest v1 in quay.io/kenosborn/does-not-exist: unauthorized: access to the requested resource is not authorized",
			expectedIs:  ErrImageNotFound,
		},
		{
			name:        "image not found disguised as unauthorized case insensitive",
			stderr:      "Error: initializing source docker://quay.io/kenosborn/does-not-exist:v1: Reading manifest v1 in quay.io/kenosborn/does-not-exist: unauthorized: access to the requested resource is not authorized",
			exitCode:    125,
			expectedErr: &stderrError{wrapped: ErrImageNotFound, reason: "manifest unknown", code: 125, stderr: "Error: initializing source docker://quay.io/kenosborn/does-not-exist:v1: Reading manifest v1 in quay.io/kenosborn/does-not-exist: unauthorized: access to the requested resource is not authorized"},
			expectedMsg: "image not found: code: 125: Error: initializing source docker://quay.io/kenosborn/does-not-exist:v1: Reading manifest v1 in quay.io/kenosborn/does-not-exist: unauthorized: access to the requested resource is not authorized",
			expectedIs:  ErrImageNotFound,
		},
		{
			name:        "actual unauthorized",
			stderr:      "unauthorized: access to the requested resource is not authorized",
			exitCode:    125,
			expectedErr: &stderrError{wrapped: ErrAuthenticationFailed, reason: "unauthorized", code: 125, stderr: "unauthorized: access to the requested resource is not authorized"},
			expectedMsg: "authentication failed: code: 125: unauthorized: access to the requested resource is not authorized",
			expectedIs:  ErrAuthenticationFailed,
		},
		{
			name:        "manifest unknown",
			stderr:      "manifest unknown",
			exitCode:    125,
			expectedErr: &stderrError{wrapped: ErrImageNotFound, reason: "manifest unknown", code: 125, stderr: "manifest unknown"},
			expectedMsg: "image not found: code: 125: manifest unknown",
			expectedIs:  ErrImageNotFound,
		},
		{
			name:          "no error",
			stderr:        "",
			exitCode:      0,
			expectedIsNil: true,
		},
		{
			name:        "generic error",
			stderr:      "some other error",
			exitCode:    1,
			expectedErr: fmt.Errorf("code: 1: some other error"),
			expectedMsg: "code: 1: some other error",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := FromStderr(tc.stderr, tc.exitCode)
			if tc.expectedIsNil {
				require.NoError(err)
			} else {
				require.Error(err)
				require.Equal(tc.expectedMsg, err.Error())
				if tc.expectedIs != nil {
					require.True(errors.Is(err, tc.expectedIs))
				}
			}
		})
	}
}
