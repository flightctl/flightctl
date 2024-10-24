package hook

import (
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/stretchr/testify/require"
)

func TestReplaceTokensInrun(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		run      string
		tokens   map[string]string
		wantErr  error
		expected string
	}{
		{
			name:     "no tokens",
			run:      "foo bar baz",
			tokens:   map[string]string{},
			expected: "foo bar baz",
		},
		{
			name:     "single token",
			run:      "foo bar baz {{ .FilePath }}",
			tokens:   map[string]string{"FilePath": "replaced"},
			expected: "foo bar baz replaced",
		},
		{
			name:     "multiple tokens",
			run:      "foo bar baz {{ .FilePath }} {{ .FilePath }}",
			tokens:   map[string]string{"FilePath": "replaced"},
			expected: "foo bar baz replaced replaced",
		},
		{
			name:     "single token no spaces",
			run:      "foo bar baz {{.FilePath}}",
			tokens:   map[string]string{"FilePath": "replaced"},
			expected: "foo bar baz replaced",
		},
		{
			name:     "token not found",
			run:      "{{ .FilePerms }} foo bar baz",
			tokens:   map[string]string{"FilePath": "replaced"},
			expected: " foo bar baz",
		},
		{
			name:     "multiple different tokens",
			run:      "{{ .FilePath }} foo bar baz {{ .FilePerms }}",
			tokens:   map[string]string{"FilePath": "replaced", "FilePerms": "0666"},
			expected: "replaced foo bar baz 0666",
		},
		{
			name:    "invalid token format",
			run:     "{{ FilePath }} foo bar baz",
			tokens:  map[string]string{"FilePath": "replaced"},
			wantErr: errors.ErrInvalidTokenFormat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := replaceTokens(tt.run, tt.tokens)
			if tt.wantErr != nil {
				require.Error(err)
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
			require.Equal(tt.expected, got)
		})
	}
}
