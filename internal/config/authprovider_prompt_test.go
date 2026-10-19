package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfiguredAuthProviderPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		field   string
		want    string
		invalid bool
	}{
		{name: "When prompt is omitted it should default to select_account", want: "select_account"},
		{name: "When prompt is null it should default to select_account", field: "prompt: null", want: "select_account"},
		{name: "When prompt is empty it should preserve the opt-out", field: `prompt: ""`},
		{name: "When prompt has extra spaces it should preserve them", field: `prompt: " login  consent "`, want: " login  consent "},
		{name: "When prompt has provider extensions it should preserve them", field: `prompt: 'login "custom" #extension'`, want: `login "custom" #extension`},
		{name: "When prompt combines none with another value it should reject it", field: `prompt: "none login"`, invalid: true},
		{name: "When prompt contains only spaces it should reject it", field: `prompt: "  "`, invalid: true},
		{name: "When prompt contains a tab it should reject it", field: `prompt: "login\tconsent"`, invalid: true},
	}
	for _, provider := range []string{"oidc", "oauth2"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					path := filepath.Join(t.TempDir(), "config.yaml")
					contents := fmt.Sprintf(`auth:
  %s:
    issuer: https://example.com
    clientId: flightctl-client
    authorizationUrl: https://example.com/authorize
    tokenUrl: https://example.com/token
    userinfoUrl: https://example.com/userinfo
    introspection:
      type: github
    roleAssignment:
      type: static
      roles: [flightctl-admin]
    %s
`, provider, tt.field)
					require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
					cfg, err := Load(path)
					require.NoError(t, err)
					err = Validate(cfg)
					if tt.invalid {
						require.ErrorContains(t, err, "auth."+provider+".prompt")
						return
					}
					require.NoError(t, err)
					var prompt *string
					if provider == "oidc" {
						prompt = cfg.Auth.OIDC.Prompt
						require.Nil(t, cfg.Auth.OAuth2)
					} else {
						prompt = cfg.Auth.OAuth2.Prompt
						require.Nil(t, cfg.Auth.OIDC)
					}
					require.NotNil(t, prompt)
					require.Equal(t, tt.want, *prompt)
				})
			}
		})
	}
	t.Run("When auth providers are absent it should leave them absent", func(t *testing.T) {
		t.Parallel()
		cfg := NewDefault()
		require.NoError(t, applyAuthDefaults(cfg))
		require.Nil(t, cfg.Auth.OIDC)
		require.Nil(t, cfg.Auth.OAuth2)
		cfg.Auth = nil
		require.NoError(t, applyAuthDefaults(cfg))
	})
}
