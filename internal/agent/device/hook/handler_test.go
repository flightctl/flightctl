package hook

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplaceTokensInrun(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		run      string
		tokens   map[CommandLineVarKey]string
		wantErr  error
		expected string
	}{
		{
			name:     "no tokens",
			run:      "foo bar baz",
			tokens:   map[CommandLineVarKey]string{},
			expected: "foo bar baz",
		},
		{
			name:     "single token",
			run:      "foo bar baz {{ Path }}",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced"},
			expected: "foo bar baz replaced",
		},
		{
			name:     "multiple tokens",
			run:      "foo bar baz {{ Path }} {{ Path }}",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced", FilesKey: "replaced"},
			expected: "foo bar baz replaced replaced",
		},
		{
			name:     "single token no spaces",
			run:      "foo bar baz {{Path}}",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced"},
			expected: "foo bar baz replaced",
		},
		{
			name:     "token not found",
			run:      "{{ DoesNotExist }} foo bar baz",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced"},
			expected: "{{ DoesNotExist }} foo bar baz",
		},
		{
			name:     "multiple different tokens",
			run:      "{{ Path }} foo bar baz {{ Files }}",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced", FilesKey: "a b c"},
			expected: "replaced foo bar baz a b c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replaceTokens(tt.run, tt.tokens)
			require.Equal(tt.expected, got)
		})
	}
}
