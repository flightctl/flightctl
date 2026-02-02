package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeleteRequiresCatalogFlag(t *testing.T) {
	// Create a minimal config file so GlobalOptions.Validate doesn't fail on missing login
	configDir := t.TempDir()
	configFile := filepath.Join(configDir, "client.yaml")
	err := os.WriteFile(configFile, []byte("{}"), 0600)
	require.NoError(t, err)

	tests := []struct {
		name        string
		args        []string
		catalogName string
		fleetName   string
		wantErr     bool
		errContains string
	}{
		{
			name:        "catalogitem without --catalog",
			args:        []string{"catalogitem", "my-item"},
			catalogName: "",
			wantErr:     true,
			errContains: "--catalog must be specified",
		},
		{
			name:        "catalogitem with --catalog",
			args:        []string{"catalogitem", "my-item"},
			catalogName: "my-catalog",
			wantErr:     false,
		},
		{
			name:        "templateversion without --fleetname",
			args:        []string{"templateversion", "my-tv"},
			wantErr:     true,
			errContains: "fleetname must be specified",
		},
		{
			name:    "catalog needs no extra flags",
			args:    []string{"catalog", "my-catalog"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			o := DefaultDeleteOptions()
			o.ConfigFilePath = configFile
			o.CatalogName = tt.catalogName
			o.FleetName = tt.fleetName
			err := o.Validate(tt.args)
			if tt.wantErr {
				require.Error(err)
				require.Contains(err.Error(), tt.errContains)
			} else {
				require.NoError(err)
			}
		})
	}
}
