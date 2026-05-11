package quadlet

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuadletServiceConfigDBType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"builtin quoted", "db:\n  type: \"builtin\"\n", "builtin"},
		{"external", "db:\n  type: external\n", "external"},
		{"missing db", "service: {}\n", ""},
		{"missing type", "db:\n  name: flightctl\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := quadletServiceConfigDBType([]byte(tc.yaml))
			require.Equal(t, tc.want, got)
		})
	}
}
