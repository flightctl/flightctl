package debugdestination

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateDefaultConfig(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig()
	c, ok := cfg.(*Config)
	require.True(t, ok)
	require.Equal(t, VerbosityBasic, c.Verbosity)
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "When verbosity is basic it should pass",
			cfg:  Config{Verbosity: VerbosityBasic},
		},
		{
			name: "When verbosity is normal it should pass",
			cfg:  Config{Verbosity: VerbosityNormal},
		},
		{
			name: "When verbosity is detailed it should pass",
			cfg:  Config{Verbosity: VerbosityDetailed},
		},
		{
			name:    "When verbosity is the zero value it should return an error",
			cfg:     Config{},
			wantErr: "unsupported verbosity",
		},
		{
			name:    "When verbosity is unsupported it should return an error",
			cfg:     Config{Verbosity: "verbose"},
			wantErr: "unsupported verbosity",
		},
		{
			name:    "When verbosity is an unknown value it should return an error",
			cfg:     Config{Verbosity: "trace"},
			wantErr: "unsupported verbosity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
