package experimental

import (
	"os"
	"testing"

	"github.com/flightctl/flightctl/test/util"
)

func TestNewFeatures(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
		want  bool
	}{
		{
			name:  "environment variable not set",
			env:   "",
			value: "",
			want:  false,
		},
		{
			name:  "environment variable set to empty string",
			env:   ExperimentalFeatureEnvKey,
			value: "",
			want:  false,
		},
		{
			name:  "environment variable set to non-empty string",
			env:   ExperimentalFeatureEnvKey,
			value: "true-ish",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetEnv := util.TestTempEnv(tt.env, tt.value)
			defer resetEnv()

			if tt.env == "" {
				os.Unsetenv(ExperimentalFeatureEnvKey)
			}

			experimentalFeatures := NewFeatures()
			if got := experimentalFeatures.IsEnabled(); got != tt.want {
				t.Errorf("NewFeatures().IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
