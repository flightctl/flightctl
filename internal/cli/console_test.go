package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConsoleOptions_Validate(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		tty         bool
		noTTY       bool
		wantErr     bool
		errContains string
	}{
		{
			name:    "When device name is provided it should succeed",
			args:    []string{"device/mydevice"},
			wantErr: false,
		},
		{
			name:    "When device name is provided with space separator it should succeed",
			args:    []string{"device", "mydevice"},
			wantErr: false,
		},
		{
			name:        "When no device name is given it should return an error",
			args:        []string{},
			wantErr:     true,
			errContains: "no arguments provided",
		},
		{
			name:        "When more than two positional arguments are given it should return an error",
			args:        []string{"device", "mydevice", "extra"},
			wantErr:     true,
			errContains: "arguments must be of the form",
		},
		{
			name:        "When --tty and --notty are both set it should return an error",
			args:        []string{"device/mydevice"},
			tty:         true,
			noTTY:       true,
			wantErr:     true,
			errContains: "--tty and --notty are mutually exclusive",
		},
		{
			name:        "When non-device kind is provided it should return an error",
			args:        []string{"fleet/myfarm"},
			wantErr:     true,
			errContains: "only devices can be connected to a console",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultConsoleOptions()
			o.tty = tt.tty
			o.noTTY = tt.noTTY

			err := o.Validate(tt.args)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
