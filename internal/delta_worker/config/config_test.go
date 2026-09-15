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
			name: "When an HTTP repository has credentials it should be accepted",
			config: &DeltaGenerationConfig{DefaultRepository: &DefaultRepositoryConfig{
				Registry: "registry.example.com",
				Scheme:   lo.ToPtr("http"),
				Username: "robot",
				Password: api.SecureString("secret"),
			}},
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
		{
			name: "When an HTTP repository has only a username it should be rejected",
			config: &DeltaGenerationConfig{DefaultRepository: &DefaultRepositoryConfig{
				Registry: "registry.example.com",
				Scheme:   lo.ToPtr("http"),
				Username: "robot",
			}},
			wantErr: "username and password must be configured together",
		},
		{
			name: "When an HTTP repository has only a password it should be rejected",
			config: &DeltaGenerationConfig{DefaultRepository: &DefaultRepositoryConfig{
				Registry: "registry.example.com",
				Scheme:   lo.ToPtr("http"),
				Password: api.SecureString("secret"),
			}},
			wantErr: "username and password must be configured together",
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

func TestDefaultRepositoryConfigOciRepoSpec(t *testing.T) {
	tests := []struct {
		name    string
		config  *DefaultRepositoryConfig
		wantErr string
	}{
		{
			name: "When both credentials are configured it should create an authenticated spec",
			config: &DefaultRepositoryConfig{
				Registry: "registry.example.com",
				Scheme:   lo.ToPtr("http"),
				Username: "robot",
				Password: api.SecureString("secret"),
			},
		},
		{
			name: "When only a username is configured it should return an error",
			config: &DefaultRepositoryConfig{
				Registry: "registry.example.com",
				Username: "robot",
			},
			wantErr: "username and password must be configured together",
		},
		{
			name: "When only a password is configured it should return an error",
			config: &DefaultRepositoryConfig{
				Registry: "registry.example.com",
				Password: api.SecureString("secret"),
			},
			wantErr: "username and password must be configured together",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.config.OciRepoSpec()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
