package util

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtendedParseDuration(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
		wantErr  bool
	}{
		// Standard Go durations should work
		{
			name:     "standard seconds",
			input:    "30s",
			expected: 30 * time.Second,
			wantErr:  false,
		},
		{
			name:     "standard minutes",
			input:    "5m",
			expected: 5 * time.Minute,
			wantErr:  false,
		},
		{
			name:     "standard hours",
			input:    "2h",
			expected: 2 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "complex standard duration",
			input:    "1h30m45s",
			expected: 1*time.Hour + 30*time.Minute + 45*time.Second,
			wantErr:  false,
		},

		// Extended durations
		{
			name:     "one day",
			input:    "1d",
			expected: 24 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "seven days (one week)",
			input:    "7d",
			expected: 168 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "one week",
			input:    "1w",
			expected: 168 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "complex non-standard duration",
			input:    "1w2d3h30m1w",
			expected: 1*Week + 2*Day + 3*time.Hour + 30*time.Minute + 1*Week,
			wantErr:  false,
		},

		// Error cases
		{
			name:    "invalid duration",
			input:   "invalid",
			wantErr: true,
		},
		{
			name:     "empty string",
			input:    "",
			expected: 0 * time.Second,
			wantErr:  false,
		},
		{
			name:     "large number of days",
			input:    "100d",
			expected: 2400 * time.Hour,
			wantErr:  false,
		},
		{
			name:    "unsupported months",
			input:   "1M",
			wantErr: true,
		},
		{
			name:    "unsupported years",
			input:   "1y",
			wantErr: true,
		},
		{
			name:    "special character as number",
			input:   "%w",
			wantErr: true,
		},
		{
			name:    "mixed valid and invalid chars",
			input:   "1!d",
			wantErr: true,
		},
		{
			name:    "negative number",
			input:   "-1d",
			wantErr: true,
		},
		{
			name:     "large string with many units",
			input:    strings.Repeat("1d", 100),
			expected: 100 * Day,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtendedParseDuration(tt.input)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}
