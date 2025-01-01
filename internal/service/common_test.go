package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertSelectorToFieldsMap(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name        string
		fieldFilter []string
		want        map[string][]any
		wantErr     error
	}{
		{
			name:        "valid key and value",
			fieldFilter: []string{"example.key=value"},
			want: map[string][]any{
				"example.key": {"value"},
			},
		},
		{
			name:        "valid key and value whitespace",
			fieldFilter: []string{" example.key=value "},
			want: map[string][]any{
				"example.key": {"value"},
			},
		},
		{
			name:        "valid value with hyphen and dot",
			fieldFilter: []string{"example.key=val-u.e"},
			want: map[string][]any{
				"example.key": {"val-u.e"},
			},
		},
		{
			name:        "invalid key",
			fieldFilter: []string{"example/key=value"},
			want:        nil,
			wantErr:     ErrorInvalidFieldKey,
		},
		{
			name:        "invalid value",
			fieldFilter: []string{"example.key=value_"},
			want:        nil,
			wantErr:     ErrorInvalidFieldValue,
		},
		{
			name: "valid key with multiple values",
			fieldFilter: []string{
				"example.key=value1",
				"example.key=value2",
				"example.key=value3",
			},
			want: map[string][]any{
				"example.key": {"value1", "value2", "value3"},
			},
		},
		{
			name: "multiple key value pairs",
			fieldFilter: []string{
				"example.key=value1",
				"example.key=value2",
				"example.key=value3",
				"example.complex.key=value4",
			},
			want: map[string][]any{
				"example.key":         {"value1", "value2", "value3"},
				"example.complex.key": {"value4"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertFieldFilterParamsToMap(tt.fieldFilter)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
			require.Equal(tt.want, got)
		})
	}
}
