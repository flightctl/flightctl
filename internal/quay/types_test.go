package quay

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCVSSUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  float64
	}{
		{name: "numeric score", input: `{"Vectors":"AV:N/AC:L","Score":9.8}`, want: 9.8},
		{name: "string score", input: `{"Vectors":"AV:N/AC:L","Score":"9.8"}`, want: 9.8},
		{name: "empty score", input: `{"Score":""}`, want: 0},
		{name: "null score", input: `{"Score":null}`, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cvss CVSS
			require.NoError(t, json.Unmarshal([]byte(tt.input), &cvss))
			require.Equal(t, tt.want, cvss.Score)
		})
	}
}
