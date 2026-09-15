package config

import (
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestDeltaGenerationConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *DeltaGenerationConfig
		wantErr string
	}{
		{
			name: "When an HTTP repository has credentials it should be rejected",
			config: &DeltaGenerationConfig{DefaultRepository: &DefaultRepositoryConfig{
				Registry: "registry.example.com",
				Scheme:   lo.ToPtr("http"),
				Username: "robot",
				Password: api.SecureString("secret"),
			}},
			wantErr: "cannot use credentials with http",
		},
		{
			name: "When an HTTP repository has no credentials it should be accepted",
			config: &DeltaGenerationConfig{DefaultRepository: &DefaultRepositoryConfig{
				Registry: "registry.example.com",
				Scheme:   lo.ToPtr("http"),
			}},
		},
		{
			name: "When an HTTPS repository has credentials it should be accepted",
			config: &DeltaGenerationConfig{DefaultRepository: &DefaultRepositoryConfig{
				Registry: "registry.example.com",
				Scheme:   lo.ToPtr("https"),
				Username: "robot",
				Password: api.SecureString("secret"),
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
