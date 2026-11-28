package config

import (
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestDeltaGenerationConfigRegistryValidation(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
		valid          bool
	}{
		{"When a registry hostname is supplied it should accept", "registry.example.com", true},
		{"When a numeric DNS label is supplied it should accept", "123.example.com:5000", true},
		{"When IPv4 is supplied it should accept", "192.0.2.1", true},
		{"When IPv4 has a port it should accept", "192.0.2.1:5000", true},
		{"When IPv6 has a port it should accept", "[2001:db8::1]:5000", true},
		{"When IPv4 octets overflow it should reject", "999.999.999.999", false},
		{"When invalid IPv4 has a port it should reject", "999.999.999.999:5000", false},
		{"When one IPv4 octet overflows it should reject", "256.1.2.3", false},
		{"When IPv4 has a leading zero it should reject", "192.168.001.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := DeltaGenerationConfig{DefaultRepository: &DefaultRepositoryConfig{Registry: tc.endpoint}}
			err := config.Validate()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "deltaGeneration.defaultRepository.registry")
			}
		})
	}
}

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

func TestDefaultRepositoryConfigOciRepoSpecWithoutRegistry(t *testing.T) {
	tests := []struct {
		name   string
		config *DefaultRepositoryConfig
	}{
		{
			name: "When no default repository is configured it should not create a target",
		},
		{
			name: "When only credentials are present it should not create a target",
			config: &DefaultRepositoryConfig{
				Username: "robot",
				Password: api.SecureString("secret"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := tt.config.OciRepoSpec()
			require.NoError(t, err)
			require.Nil(t, spec)
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
			spec, err := tt.config.OciRepoSpec()
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.NotNil(t, spec)
				require.Equal(t, "registry.example.com", spec.Registry)
				require.NotNil(t, spec.OciAuth)
				auth, err := spec.OciAuth.AsDockerAuth()
				require.NoError(t, err)
				require.Equal(t, api.DockerAuth{AuthType: "docker", Username: "robot", Password: "secret"}, auth)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
