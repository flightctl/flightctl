package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertSelectorToFieldsMap(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name         string
		statusFilter []string
		want         map[string][]string
		wantErr      error
	}{
		{
			name:         "valid key and value",
			statusFilter: []string{"example.key=value"},
			want: map[string][]string{
				"example.key": {"value"},
			},
		},
		{
			name:         "valid key and value whitespace",
			statusFilter: []string{" example.key=value "},
			want: map[string][]string{
				"example.key": {"value"},
			},
		},
		{
			name:         "invalid key",
			statusFilter: []string{"example/key=value"},
			want:         nil,
			wantErr:      ErrorInvalidFieldKey,
		},
		{
			name:         "invalid value",
			statusFilter: []string{"example.key=value_"},
			want:         nil,
			wantErr:      ErrorInvalidFieldValue,
		},
		{
			name: "valid key with multiple values",
			statusFilter: []string{
				"example.key=value1",
				"example.key=value2",
				"example.key=value3",
			},
			want: map[string][]string{
				"example.key": {"value1", "value2", "value3"},
			},
		},
		{
			name: "multiple key value pairs",
			statusFilter: []string{
				"example.key=value1",
				"example.key=value2",
				"example.key=value3",
				"example.complex.key=value4",
			},
			want: map[string][]string{
				"example.key":         {"value1", "value2", "value3"},
				"example.complex.key": {"value4"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertStatusFilterParamsToMap(tt.statusFilter)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
			require.Equal(tt.want, got)
		})
	}
}
