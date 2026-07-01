package authprovider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/stretchr/testify/require"
)

// TestBuildOAuth2AuthProviderForDeploymentSelectsPAMIssuer verifies PAM-backed OAuth2 is selected when the auth config advertises the PAM issuer.
func TestBuildOAuth2AuthProviderForDeploymentSelectsPAMIssuer(t *testing.T) {
	authConfig := authConfigWithProviders(t, authProviderWithOIDCSpec(t, "custom-pam", "https://pam-issuer.example.com/api/v1/auth", testFixtureValue("pam", "client")))
	server := authConfigServer(t, authConfig)
	defer server.Close()

	renderedYAML, usePAMOAuth2, err := buildOAuth2AuthProviderForDeployment(
		context.Background(),
		server.URL,
		"oauth2-test",
		"https://keycloak.example.com/realms/e2e",
		"keycloak-client",
		testFixtureValue("keycloak", "client"),
	)

	require.NoError(t, err)
	require.True(t, usePAMOAuth2)
	require.Contains(t, renderedYAML, "https://pam-issuer.example.com/api/v1/auth")
	require.NotContains(t, renderedYAML, "https://keycloak.example.com/realms/e2e")
}

// TestBuildOAuth2AuthProviderForDeploymentFallsBackToKeycloak verifies Keycloak OAuth2 is used when no PAM issuer is advertised.
func TestBuildOAuth2AuthProviderForDeploymentFallsBackToKeycloak(t *testing.T) {
	authConfig := authConfigWithProviders(t, authProviderWithOIDCSpec(t, "oidc", "https://sso.example.com/realms/e2e", testFixtureValue("oidc", "client")))
	server := authConfigServer(t, authConfig)
	defer server.Close()

	renderedYAML, usePAMOAuth2, err := buildOAuth2AuthProviderForDeployment(
		context.Background(),
		server.URL,
		"oauth2-test",
		"https://keycloak.example.com/realms/e2e",
		"keycloak-client",
		testFixtureValue("keycloak", "client"),
	)

	require.NoError(t, err)
	require.False(t, usePAMOAuth2)
	require.Contains(t, renderedYAML, "https://keycloak.example.com/realms/e2e")
}

// TestResolvePAMClientSecretFromInfra verifies PAM client value resolution from public config and infra service config.
func TestResolvePAMClientSecretFromInfra(t *testing.T) {
	publicValue := testFixtureValue("public", "client")
	serviceValue := testFixtureValue("service", "client")
	tests := []struct {
		name        string
		secret      string
		provider    infra.InfraProvider
		want        string
		wantErrText string
	}{
		{
			name:   "When the public client value is unmasked it should return the public client value",
			secret: publicValue,
			want:   publicValue,
		},
		{
			name:        "When the client value is masked and infra provider is missing it should return an error",
			secret:      maskedSecretValue,
			wantErrText: "infra provider is required",
		},
		{
			name:   "When the client value is masked it should return the service config value",
			secret: maskedSecretValue,
			provider: fakeInfraProvider{
				serviceConfig: fmt.Sprintf("auth:\n  oidc:\n    clientSecret: %s\n", serviceValue),
			},
			want: serviceValue,
		},
		{
			name:   "When service config lookup fails it should wrap the lookup error",
			secret: maskedSecretValue,
			provider: fakeInfraProvider{
				serviceConfigErr: errors.New("boom"),
			},
			wantErrText: "read API service config",
		},
		{
			name:   "When the service config value is unset it should use the deterministic placeholder",
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

// TestIsPAMIssuer verifies PAM issuer matching uses exact host labels or path segments.
func TestIsPAMIssuer(t *testing.T) {
	tests := []struct {
		name   string
		issuer string
		want   bool
	}{
		{name: "When the hostname has a PAM issuer label it should match", issuer: "https://pam-issuer.example.com/api/v1/auth", want: true},
		{name: "When the hostname has a PAM issuer label and port it should match", issuer: "https://pam-issuer.example.com:8080/auth", want: true},
		{name: "When the path has a PAM issuer segment it should match", issuer: "https://auth.example.com/pam-issuer/api/v1/auth", want: true},
		{name: "When the path has a PAM issuer segment and query params it should match", issuer: "https://auth.example.com/pam-issuer?foo=bar", want: true},
		{name: "When the path has double slashes before a PAM issuer segment it should match", issuer: "https://auth.example.com//pam-issuer/auth", want: true},
		{name: "When the hostname only contains a PAM issuer substring it should not match", issuer: "https://spam-issuer.example.com/api/v1/auth", want: false},
		{name: "When the path only contains a PAM issuer substring it should not match", issuer: "https://auth.example.com/spam-issuer/api/v1/auth", want: false},
		{name: "When the issuer is empty it should not match", issuer: "", want: false},
		{name: "When the issuer is whitespace only it should not match", issuer: "   ", want: false},
		{name: "When the issuer URL is invalid it should not match", issuer: "://bad-url", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isPAMIssuer(tt.issuer))
		})
	}
}

// TestSanitizedAuthURLForLog verifies OAuth query and fragment data are removed before auth URLs are logged.
func TestSanitizedAuthURLForLog(t *testing.T) {
	nonceValue := testFixtureValue("nonce", "value")
	require.Equal(t,
		"https://keycloak.example.com/realms/e2e/protocol/openid-connect/auth",
		sanitizedAuthURLForLog(fmt.Sprintf("https://keycloak.example.com/realms/e2e/protocol/openid-connect/auth?state=sensitive&nonce=%s#fragment", nonceValue)),
	)
	require.Equal(t, "<invalid auth URL>", sanitizedAuthURLForLog("://bad-url"))
}

// TestRedactAuthProviderCredentialsRedactsOAuthURLSecrets verifies OAuth URL values are redacted before command output is logged.
func TestRedactAuthProviderCredentialsRedactsOAuthURLSecrets(t *testing.T) {
	stateValue := testFixtureValue("state", "value")
	nonceValue := testFixtureValue("nonce", "value")
	codeValue := testFixtureValue("code", "value")
	sessionStateValue := testFixtureValue("session", "value")
	accessTokenValue := testFixtureValue("access", "value")
	output := fmt.Sprintf(`Please open this URL in your browser: https://keycloak.example.com/auth?client_id=flightctl&state=%s&nonce=%s&code=%s
callback: http://localhost:8080/callback?session_state=%s&access_token=%s`, stateValue, nonceValue, codeValue, sessionStateValue, accessTokenValue)

	redacted := redactAuthProviderCredentials(output)

	require.NotContains(t, redacted, stateValue)
	require.NotContains(t, redacted, nonceValue)
	require.NotContains(t, redacted, codeValue)
	require.NotContains(t, redacted, sessionStateValue)
	require.NotContains(t, redacted, accessTokenValue)
	require.Contains(t, redacted, "state=<REDACTED>")
	require.Contains(t, redacted, "nonce=<REDACTED>")
	require.Contains(t, redacted, "code=<REDACTED>")
	require.Contains(t, redacted, "session_state=<REDACTED>")
	require.Contains(t, redacted, "access_token=<REDACTED>")
}

// TestLoginCommandPipesClosesStdoutWhenStderrPipeFails verifies partial pipe setup is cleaned up on later setup errors.
func TestLoginCommandPipesClosesStdoutWhenStderrPipeFails(t *testing.T) {
	cmd := exec.Command("echo")
	cmd.Stderr = io.Discard

	stdoutPipe, stderrPipe, err := loginCommandPipes(cmd)

	require.Error(t, err)
	require.Nil(t, stdoutPipe)
	require.Nil(t, stderrPipe)
	require.Contains(t, err.Error(), "Stderr already set")
	require.Nil(t, cmd.Stdout)
}

// authConfigServer serves the given AuthConfig at the path used by the CLI auth config endpoint.
func authConfigServer(t *testing.T, authConfig api.AuthConfig) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/config" {
			http.Error(w, "unexpected auth config path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(authConfig); err != nil {
			http.Error(w, "encode auth config", http.StatusInternalServerError)
			return
		}
	}))
}

// testFixtureValue builds deterministic non-credential fixture values without embedding credential-like literals.
func testFixtureValue(parts ...string) string {
	return strings.Join(append([]string{"fixture"}, parts...), "-")
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

// fakeInfraProvider implements infra.InfraProvider for helper tests that only need service config access.
type fakeInfraProvider struct {
	serviceConfig    string
	serviceConfigErr error
}

// GetConfigValue is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) GetConfigValue(string, string) (string, error) {
	return "", errors.New("not implemented")
}

// GetServiceConfig returns the configured fake service config or error.
func (f fakeInfraProvider) GetServiceConfig(infra.ServiceName) (string, error) {
	return f.serviceConfig, f.serviceConfigErr
}

// GetSecretValue is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) GetSecretValue(string, string) (string, error) {
	return "", errors.New("not implemented")
}

// GetServiceEndpoint is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) GetServiceEndpoint(infra.ServiceName) (string, int, error) {
	return "", 0, errors.New("not implemented")
}

// ExposeService is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) ExposeService(infra.ServiceName, string) (string, func(), error) {
	return "", func() {}, errors.New("not implemented")
}

// InvalidateExposeCache is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) InvalidateExposeCache(infra.ServiceName) {}

// ExecInService is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) ExecInService(infra.ServiceName, []string) (string, error) {
	return "", errors.New("not implemented")
}

// ExecInServiceWithStdin is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) ExecInServiceWithStdin(infra.ServiceName, []string, io.Reader) (string, error) {
	return "", errors.New("not implemented")
}

// GetEnvironmentType is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) GetEnvironmentType() string {
	return ""
}

// GetAPILoginToken is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) GetAPILoginToken() (string, error) {
	return "", errors.New("not implemented")
}

// SetServiceConfig is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) SetServiceConfig(infra.ServiceName, string, string) error {
	return errors.New("not implemented")
}

// GetInternalNamespace is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) GetInternalNamespace() string {
	return ""
}

// GetExternalNamespace is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) GetExternalNamespace() string {
	return ""
}

// BuiltinDatabaseWorkloadAvailable is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) BuiltinDatabaseWorkloadAvailable() bool {
	return false
}

var _ infra.InfraProvider = fakeInfraProvider{}
