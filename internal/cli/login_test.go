package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureURLScheme(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "hostname only",
			input:    "example.com",
			expected: "https://example.com",
		},
		{
			name:     "hostname with port",
			input:    "example.com:8080",
			expected: "https://example.com:8080",
		},
		{
			name:     "https scheme present",
			input:    "https://example.com",
			expected: "https://example.com",
		},
		{
			name:     "http scheme present",
			input:    "http://example.com",
			expected: "http://example.com",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "localhost",
			input:    "localhost:8000",
			expected: "https://localhost:8000",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := ensureURLScheme(tc.input)
			require.Equal(t, tc.expected, actual)
		})
	}
}