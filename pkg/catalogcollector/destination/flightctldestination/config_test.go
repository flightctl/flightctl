package flightctldestination

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/stretchr/testify/require"
)

func TestCreateDefaultConfig(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig()
	c, ok := cfg.(*Config)
	require.True(t, ok)
	require.Empty(t, c.Server)
	require.Equal(t, 30*time.Second, c.timeoutDuration())
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "When all required fields are set it should pass",
			cfg:  Config{Server: "https://api.example.com"},
		},
		{
			name:    "When server is missing it should return an error",
			cfg:     Config{},
			wantErr: `missing required field "server"`,
		},
		{
			name: "When unauthenticated access is configured it should pass",
			cfg:  Config{Server: "https://api.example.com"},
		},
		{
			name: "When auth is nil it should pass",
			cfg:  Config{Server: "https://api.example.com", Auth: nil},
		},
		{
			name:    "When auth is configured with empty authenticator it should return an error",
			cfg:     Config{Server: "https://api.example.com", Auth: &AuthConfig{Authenticator: ""}},
			wantErr: "auth.authenticator must not be empty when auth is configured",
		},
		{
			name: "When auth is configured with a valid authenticator it should pass",
			cfg:  Config{Server: "https://api.example.com", Auth: &AuthConfig{Authenticator: "bearertokenauth/my-token"}},
		},
		{
			name: "When orgId is omitted it should pass",
			cfg:  Config{Server: "https://api.example.com"},
		},
		{
			name: "When orgId is a valid UUID it should pass",
			cfg:  Config{Server: "https://api.example.com", OrgID: "00000000-0000-0000-0000-000000000000"},
		},
		{
			name:    "When orgId is not a valid UUID it should return an error",
			cfg:     Config{Server: "https://api.example.com", OrgID: "not-a-uuid"},
			wantErr: "orgId",
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

func TestConfigTimeoutDuration(t *testing.T) {
	t.Run("When timeout is nil it should return default", func(t *testing.T) {
		c := Config{Server: "https://example.com"}
		require.Equal(t, 30*time.Second, c.timeoutDuration())
	})

	t.Run("When timeout is set it should return configured value", func(t *testing.T) {
		d := util.Duration(2 * time.Minute)
		c := Config{Server: "https://example.com", Timeout: &d}
		require.Equal(t, 2*time.Minute, c.timeoutDuration())
	})
}
