package errors

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitWrapped(t *testing.T) {
	testCases := []struct {
		name          string
		err           error
		expectedFirst error
		expectedRest  error
	}{
		{
			name:          "extracts first from joined pair",
			err:           fmt.Errorf("%w: %w", ErrPhasePreparing, ErrNetwork),
			expectedFirst: ErrPhasePreparing,
			expectedRest:  ErrNetwork,
		},
		{
			name:          "nil error",
			err:           nil,
			expectedFirst: nil,
			expectedRest:  nil,
		},
		{
			name:          "non-joined error returns as rest",
			err:           ErrNetwork,
			expectedFirst: nil,
			expectedRest:  ErrNetwork,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			first, rest := splitWrapped(tc.err)
			require.Equal(tc.expectedFirst, first)
			require.Equal(tc.expectedRest, rest)
		})
	}
}

func TestFormatError(t *testing.T) {
	testCases := []struct {
		name              string
		err               error
		expectedPhase     error
		expectedComponent error
		expectedCause     error
		expectedCategory  Category
	}{
		{
			name: "extracts phase and component",
			err: fmt.Errorf("%w: %w", ErrPhasePreparing,
				fmt.Errorf("%w: %w", ErrComponentApplications, ErrNetwork)),
			expectedPhase:     ErrPhasePreparing,
			expectedComponent: ErrComponentApplications,
			expectedCause:     ErrNetwork,
			expectedCategory:  CategoryNetwork,
		},
		{
			name:              "only one level of wrapping",
			err:               fmt.Errorf("%w: %w", ErrComponentApplications, ErrNetwork),
			expectedPhase:     ErrComponentApplications,
			expectedComponent: nil,
			expectedCause:     ErrNetwork,
			expectedCategory:  CategoryNetwork,
		},
		{
			name:              "plain error",
			err:               ErrNetwork,
			expectedPhase:     nil,
			expectedComponent: nil,
			expectedCause:     ErrNetwork,
			expectedCategory:  CategoryNetwork,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			se := FormatError(tc.err)
			require.Equal(tc.expectedPhase, se.Phase)
			require.Equal(tc.expectedComponent, se.Component)
			require.Equal(tc.expectedCategory, se.Category)
		})
	}
}

func TestGetElement(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "extracts element from direct wrap",
			err:      fmt.Errorf("creating directory %w: %w", WithElement("/var/lib/myapp"), ErrPermissionDenied),
			expected: "/var/lib/myapp",
		},
		{
			name:     "extracts element from nested chain",
			err:      fmt.Errorf("%w: %w", ErrPhasePreparing, fmt.Errorf("%w: %w", ErrComponentApplications, fmt.Errorf("installing %w: %w", WithElement("nginx"), ErrNetwork))),
			expected: "nginx",
		},
		{
			name:     "no element returns empty string",
			err:      fmt.Errorf("%w: %w", ErrPhasePreparing, ErrNetwork),
			expected: "",
		},
		{
			name:     "nil error returns empty string",
			err:      nil,
			expected: "",
		},
		{
			name:     "element survives Join",
			err:      Join(ErrNetwork, WithElement("app-name")),
			expected: "app-name",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			require.Equal(tc.expected, GetElement(tc.err))
		})
	}
}

func TestFormatErrorWithElement(t *testing.T) {
	testCases := []struct {
		name            string
		err             error
		expectedElement string
	}{
		{
			name: "extracts element from full chain",
			err: fmt.Errorf("%w: %w", ErrPhasePreparing,
				fmt.Errorf("%w: %w", ErrComponentApplications,
					fmt.Errorf("parsing compose spec %w: %w", WithElement("myapp"), ErrParsingComposeSpec))),
			expectedElement: "myapp",
		},
		{
			name: "no element in chain",
			err: fmt.Errorf("%w: %w", ErrPhasePreparing,
				fmt.Errorf("%w: %w", ErrComponentApplications, ErrNetwork)),
			expectedElement: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			se := FormatError(tc.err)
			require.Equal(tc.expectedElement, se.Element)
		})
	}
}

func TestMessage(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		contains []string
	}{
		{
			name: "full chain",
			err: fmt.Errorf("%w: %w", ErrPhasePreparing,
				fmt.Errorf("%w: %w", ErrComponentApplications, ErrNetwork)),
			contains: []string{"While Preparing", "applications failed:", "service unavailable"},
		},
		{
			name: "with nested error message",
			err: fmt.Errorf("%w: %w", ErrPhasePreparing,
				fmt.Errorf("%w: %w", ErrComponentApplications,
					fmt.Errorf("installing %w: %w", WithElement("myapp.service"), ErrNetwork))),
			contains: []string{"While Preparing", "applications failed for", "myapp.service", "service unavailable"},
		},
		{
			name: "permission denied",
			err: fmt.Errorf("%w: %w", ErrPhaseApplyingUpdate,
				fmt.Errorf("%w: %w", ErrComponentConfig, ErrPermissionDenied)),
			contains: []string{"While ApplyingUpdate", "config failed:", "permission denied"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			se := FormatError(tc.err)
			msg := se.Message()

			for _, s := range tc.contains {
				require.True(strings.Contains(msg, s), "expected %q in %q", s, msg)
			}
		})
	}
}
