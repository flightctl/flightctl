package lifecycle

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactSecrets(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		input    string
		contains []string
		absent   []string
	}{
		{
			name:     "When input has a PEM private key it should be redacted",
			input:    "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----\nafter",
			contains: []string{"before", "after", "[REDACTED PEM BLOCK]"},
			absent:   []string{"MIIEpAIBAAKCAQEA"},
		},
		{
			name:     "When input has a PEM certificate it should be redacted",
			input:    "line1\n-----BEGIN CERTIFICATE-----\nMIID...\n-----END CERTIFICATE-----\nline2",
			contains: []string{"line1", "line2", "[REDACTED PEM BLOCK]"},
			absent:   []string{"MIID"},
		},
		{
			name:     "When input has a Bearer token it should be redacted",
			input:    "auth: Bearer eyJhbGciOiJSUzI1NiIs",
			contains: []string{"Bearer [REDACTED]"},
			absent:   []string{"eyJhbGciOiJSUzI1NiIs"},
		},
		{
			name:     "When input has known token env vars it should redact the values",
			input:    "TOKEN=my-secret-value\nAWS_SECRET_ACCESS_KEY=AKIA1234\nPASSWORD=hunter2",
			contains: []string{"TOKEN=[REDACTED]", "AWS_SECRET_ACCESS_KEY=[REDACTED]", "PASSWORD=[REDACTED]"},
			absent:   []string{"my-secret-value", "AKIA1234", "hunter2"},
		},
		{
			name:     "When input has suffixed secret env var names it should redact the values",
			input:    "GITHUB_TOKEN=ghp_secret\nDB_PASSWORD=supersecret",
			contains: []string{"GITHUB_TOKEN=[REDACTED]", "DB_PASSWORD=[REDACTED]"},
			absent:   []string{"ghp_secret", "supersecret"},
		},
		{
			name:     "When input has no secrets it should be preserved unchanged",
			input:    "normal output line 1\nnormal output line 2",
			contains: []string{"normal output line 1", "normal output line 2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := redactSecrets(tt.input)
			for _, s := range tt.contains {
				require.Contains(result, s)
			}
			for _, s := range tt.absent {
				require.NotContains(result, s)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	require := require.New(t)

	t.Run("When output is within limit it should be unchanged", func(t *testing.T) {
		input := "short output"
		result := truncateOutput(input, 4096)
		require.Equal(input, result)
	})

	t.Run("When output exceeds limit it should be truncated with marker at end", func(t *testing.T) {
		input := strings.Repeat("A", 5000)
		result := truncateOutput(input, 4096)
		require.LessOrEqual(len(result), 4096)
		require.True(strings.HasSuffix(result, truncationMarker),
			"output should end with truncation marker")
	})

	t.Run("When output is exactly at limit it should be unchanged", func(t *testing.T) {
		input := strings.Repeat("B", 4096)
		result := truncateOutput(input, 4096)
		require.Equal(input, result)
	})

	t.Run("When output exceeds limit it should keep the last bytes before marker", func(t *testing.T) {
		// 5000 bytes of 'A' then 'Z' at the end
		input := strings.Repeat("A", 4999) + "Z"
		result := truncateOutput(input, 4096)
		require.True(strings.HasSuffix(result, truncationMarker))
		require.Contains(result, "Z") // last byte should be preserved (near end)
	})
}
