package authprovider_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/stretchr/testify/require"
)

func TestBuildOAuth2AuthProviderForDeploymentSelectsPAMIssuer(t *testing.T) {
	authConfig := authConfigWithProviders(t, authProviderWithOIDCSpec(t, "custom-pam", "https://pam-issuer.example.com/api/v1/auth", "pam-secret"))
	server := authConfigServer(t, authConfig)
	defer server.Close()

	renderedYAML, usePAMOAuth2, err := buildOAuth2AuthProviderForDeployment(
		context.Background(),
		server.URL,
		"oauth2-test",
		"https://keycloak.example.com/realms/e2e",
		"keycloak-client",
		"keycloak-secret",
	)

	require.NoError(t, err)
	require.True(t, usePAMOAuth2)
	require.Contains(t, renderedYAML, "https://pam-issuer.example.com/api/v1/auth")
	require.NotContains(t, renderedYAML, "https://keycloak.example.com/realms/e2e")
}

func TestBuildOAuth2AuthProviderForDeploymentFallsBackToKeycloak(t *testing.T) {
	authConfig := authConfigWithProviders(t, authProviderWithOIDCSpec(t, "oidc", "https://sso.example.com/realms/e2e", "oidc-secret"))
	server := authConfigServer(t, authConfig)
	defer server.Close()

	renderedYAML, usePAMOAuth2, err := buildOAuth2AuthProviderForDeployment(
		context.Background(),
		server.URL,
		"oauth2-test",
		"https://keycloak.example.com/realms/e2e",
		"keycloak-client",
		"keycloak-secret",
	)

	require.NoError(t, err)
	require.False(t, usePAMOAuth2)
	require.Contains(t, renderedYAML, "https://keycloak.example.com/realms/e2e")
}

func TestResolvePAMClientSecretFromInfra(t *testing.T) {
	tests := []struct {
		name        string
		secret      string
		provider    infra.InfraProvider
		want        string
		wantErrText string
	}{
		{
			name:   "returns public secret when unmasked",
			secret: "public-secret",
			want:   "public-secret",
		},
		{
			name:        "requires infra provider for masked secret",
			secret:      maskedSecretValue,
			wantErrText: "infra provider is required",
		},
		{
			name:   "returns service config secret",
			secret: maskedSecretValue,
			provider: fakeInfraProvider{
				serviceConfig: "auth:\n  oidc:\n    clientSecret: real-secret\n",
			},
			want: "real-secret",
		},
		{
			name:   "wraps service config error",
			secret: maskedSecretValue,
			provider: fakeInfraProvider{
				serviceConfigErr: errors.New("boom"),
			},
			wantErrText: "read API service config for PAM client secret",
		},
		{
			name:   "uses deterministic placeholder when service secret is unset",
			secret: maskedSecretValue,
			provider: fakeInfraProvider{
				serviceConfig: "auth:\n  oidc: {}\n",
			},
			want: publicClientPlaceholder,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePAMClientSecretFromInfra(tt.secret, tt.provider)
			if tt.wantErrText != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrText)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIsPAMIssuer(t *testing.T) {
	tests := []struct {
		name   string
		issuer string
		want   bool
	}{
		{name: "hostname label match", issuer: "https://pam-issuer.example.com/api/v1/auth", want: true},
		{name: "hostname label match with port", issuer: "https://pam-issuer.example.com:8080/auth", want: true},
		{name: "path segment match", issuer: "https://auth.example.com/pam-issuer/api/v1/auth", want: true},
		{name: "path segment match with query params", issuer: "https://auth.example.com/pam-issuer?foo=bar", want: true},
		{name: "path segment match with double slashes", issuer: "https://auth.example.com//pam-issuer/auth", want: true},
		{name: "hostname substring does not match", issuer: "https://spam-issuer.example.com/api/v1/auth", want: false},
		{name: "path substring does not match", issuer: "https://auth.example.com/spam-issuer/api/v1/auth", want: false},
		{name: "empty string does not match", issuer: "", want: false},
		{name: "whitespace only does not match", issuer: "   ", want: false},
		{name: "invalid URL does not match", issuer: "://bad-url", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isPAMIssuer(tt.issuer))
		})
	}
}

func TestSanitizedAuthURLForLog(t *testing.T) {
	require.Equal(t,
		"https://keycloak.example.com/realms/e2e/protocol/openid-connect/auth",
		sanitizedAuthURLForLog("https://keycloak.example.com/realms/e2e/protocol/openid-connect/auth?state=sensitive&nonce=secret#fragment"),
	)
	require.Equal(t, "<invalid auth URL>", sanitizedAuthURLForLog("://bad-url"))
}

func TestRedactAuthProviderCredentialsRedactsOAuthURLSecrets(t *testing.T) {
	output := `Please open this URL in your browser: https://keycloak.example.com/auth?client_id=flightctl&state=abc123&nonce=def456&code=ghi789
callback: http://localhost:8080/callback?session_state=session-secret&access_token=token-secret`

	redacted := redactAuthProviderCredentials(output)

	require.NotContains(t, redacted, "abc123")
	require.NotContains(t, redacted, "def456")
	require.NotContains(t, redacted, "ghi789")
	require.NotContains(t, redacted, "session-secret")
	require.NotContains(t, redacted, "token-secret")
	require.Contains(t, redacted, "state=<REDACTED>")
	require.Contains(t, redacted, "nonce=<REDACTED>")
	require.Contains(t, redacted, "code=<REDACTED>")
	require.Contains(t, redacted, "session_state=<REDACTED>")
	require.Contains(t, redacted, "access_token=<REDACTED>")
}

// authConfigServer serves the given AuthConfig at the path used by the CLI auth config endpoint.
func authConfigServer(t *testing.T, authConfig api.AuthConfig) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/auth/config", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(authConfig))
	}))
}

// authConfigWithProviders builds a public auth config response for helper tests.
func authConfigWithProviders(t *testing.T, providers ...api.AuthProvider) api.AuthConfig {
	t.Helper()
	return api.AuthConfig{
		ApiVersion: api.AuthConfigAPIVersion,
		Providers:  &providers,
	}
}

// authProviderWithOIDCSpec builds an OIDC auth provider for helper tests.
func authProviderWithOIDCSpec(t *testing.T, name, issuer, clientSecret string) api.AuthProvider {
	t.Helper()
	providerName := name
	enabled := true
	provider := api.AuthProvider{
		Metadata: api.ObjectMeta{Name: &providerName},
		Spec:     api.AuthProviderSpec{},
	}
	require.NoError(t, provider.Spec.FromOIDCProviderSpec(api.OIDCProviderSpec{
		Issuer:       issuer,
		ClientId:     name + "-client",
		ClientSecret: clientSecret,
		Enabled:      &enabled,
	}))
	return provider
}

type fakeInfraProvider struct {
	serviceConfig    string
	serviceConfigErr error
}

func (f fakeInfraProvider) GetConfigValue(string, string) (string, error) {
	return "", errors.New("not implemented")
}

func (f fakeInfraProvider) GetServiceConfig(infra.ServiceName) (string, error) {
	return f.serviceConfig, f.serviceConfigErr
}

func (f fakeInfraProvider) GetSecretValue(string, string) (string, error) {
	return "", errors.New("not implemented")
}

func (f fakeInfraProvider) GetServiceEndpoint(infra.ServiceName) (string, int, error) {
	return "", 0, errors.New("not implemented")
}

func (f fakeInfraProvider) ExposeService(infra.ServiceName, string) (string, func(), error) {
	return "", func() {}, errors.New("not implemented")
}

func (f fakeInfraProvider) InvalidateExposeCache(infra.ServiceName) {}

func (f fakeInfraProvider) ExecInService(infra.ServiceName, []string) (string, error) {
	return "", errors.New("not implemented")
}

func (f fakeInfraProvider) ExecInServiceWithStdin(infra.ServiceName, []string, io.Reader) (string, error) {
	return "", errors.New("not implemented")
}

func (f fakeInfraProvider) GetEnvironmentType() string {
	return ""
}

func (f fakeInfraProvider) GetAPILoginToken() (string, error) {
	return "", errors.New("not implemented")
}

func (f fakeInfraProvider) SetServiceConfig(infra.ServiceName, string, string) error {
	return errors.New("not implemented")
}

func (f fakeInfraProvider) GetInternalNamespace() string {
	return ""
}

func (f fakeInfraProvider) GetExternalNamespace() string {
	return ""
}

func (f fakeInfraProvider) BuiltinDatabaseWorkloadAvailable() bool {
	return false
}

var _ infra.InfraProvider = fakeInfraProvider{}
