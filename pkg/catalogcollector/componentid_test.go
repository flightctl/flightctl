package catalogcollector

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseComponentID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantType ComponentType
		wantName string
		wantErr  string
	}{
		{
			name:     "unnamed instance",
			input:    "http",
			wantType: "http",
			wantName: "",
		},
		{
			name:     "named instance",
			input:    "http/dev-input",
			wantType: "http",
			wantName: "dev-input",
		},
		{
			name:     "named destination",
			input:    "flightctl/local",
			wantType: "flightctl",
			wantName: "local",
		},
		{
			name:     "named processor",
			input:    "filter/approved-models",
			wantType: "filter",
			wantName: "approved-models",
		},
		{
			name:    "empty identifier",
			input:   "",
			wantErr: "empty component identifier",
		},
		{
			name:    "empty type",
			input:   "/name",
			wantErr: "has empty type",
		},
		{
			name:    "empty name after separator",
			input:   "http/",
			wantErr: "has empty name after separator",
		},
		{
			name:    "multiple separators",
			input:   "a/b/c",
			wantErr: "contains multiple separators",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := ParseComponentID(tt.input)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantType, id.Type)
			require.Equal(t, tt.wantName, id.Name)
		})
	}
}

func TestComponentID_String(t *testing.T) {
	tests := []struct {
		name string
		id   ComponentID
		want string
	}{
		{
			name: "unnamed",
			id:   ComponentID{Type: "http"},
			want: "http",
		},
		{
			name: "named",
			id:   ComponentID{Type: "http", Name: "dev-input"},
			want: "http/dev-input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.id.String())
		})
	}
}

func TestParseComponentID_RoundTrip(t *testing.T) {
	for _, input := range []string{"http", "http/dev-input", "flightctl/local"} {
		id, err := ParseComponentID(input)
		require.NoError(t, err)
		require.Equal(t, input, id.String())
	}
}
