package authprovider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestBuildOAuth2AuthProviderForDeploymentSelectsPAMIssuer verifies PAM-backed OAuth2 is selected when service config enables the PAM issuer.
func TestBuildOAuth2AuthProviderForDeploymentSelectsPAMIssuer(t *testing.T) {
	pamIssuerURL := "https://auth.example.com/custom/api/v1/auth"
	authConfig := authConfigWithProviders(t, authProviderWithOIDCSpec(t, staticOIDCProviderName, pamIssuerURL, testFixtureValue("pam", "client")))
	server := authConfigServer(t, authConfig)
	defer server.Close()

	renderedYAML, usePAMOAuth2, err := buildOAuth2AuthProviderForDeploymentWithInfra(
		context.Background(),
		fakeInfraProvider{serviceConfig: "auth:\n  pamOidcIssuer:\n    enabled: true\n"},
		server.URL,
		"oauth2-test",
		"https://keycloak.example.com/realms/e2e",
		"keycloak-client",
		testFixtureValue("keycloak", "client"),
	)

	require.NoError(t, err)
	require.True(t, usePAMOAuth2)
	require.Contains(t, renderedYAML, pamIssuerURL)
	require.NotContains(t, renderedYAML, "https://keycloak.example.com/realms/e2e")
}

// TestBuildOAuth2AuthProviderForDeploymentFallsBackToKeycloak verifies Keycloak OAuth2 is used when PAM issuer is not configured.
func TestBuildOAuth2AuthProviderForDeploymentFallsBackToKeycloak(t *testing.T) {
	authConfig := authConfigWithProviders(t, authProviderWithOIDCSpec(t, staticOIDCProviderName, "https://pam-issuer.example.com/api/v1/auth", testFixtureValue("oidc", "client")))
	server := authConfigServer(t, authConfig)
	defer server.Close()

	renderedYAML, usePAMOAuth2, err := buildOAuth2AuthProviderForDeploymentWithInfra(
		context.Background(),
		fakeInfraProvider{serviceConfig: "auth:\n  oidc: {}\n"},
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

// TestDeploymentPAMOIDCIssuerConfigured verifies PAM detection uses service config rather than issuer URL heuristics.
func TestDeploymentPAMOIDCIssuerConfigured(t *testing.T) {
	tests := []struct {
		name          string
		serviceConfig string
		provider      infra.InfraProvider
		want          bool
		wantErrText   string
	}{
		{name: "When PAM issuer config exists it should report PAM configured", serviceConfig: "auth:\n  pamOidcIssuer: {}\n", want: true},
		{name: "When PAM issuer config is enabled it should report PAM configured", serviceConfig: "auth:\n  pamOidcIssuer:\n    enabled: true\n", want: true},
		{name: "When PAM issuer config is disabled it should report PAM not configured", serviceConfig: "auth:\n  pamOidcIssuer:\n    enabled: false\n", want: false},
		{name: "When PAM issuer config is missing it should report PAM not configured", serviceConfig: "auth:\n  oidc: {}\n", want: false},
		{name: "When infra provider is missing it should return an error", wantErrText: "infra provider is required"},
		{
			name: "When service config lookup fails it should return an error",
			provider: fakeInfraProvider{
				serviceConfigErr: errors.New("boom"),
			},
			wantErrText: "read API service config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := tt.provider
			if provider == nil && tt.serviceConfig != "" {
				provider = fakeInfraProvider{serviceConfig: tt.serviceConfig}
			}

			got, err := deploymentPAMOIDCIssuerConfigured(provider)
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

// TestWithQuadletPAMRedirectURI verifies PAM redirect URI mutation behavior without infra dependencies.
func TestWithQuadletPAMRedirectURI(t *testing.T) {
	tests := []struct {
		name          string
		serviceConfig string
		callbackURI   string
		wantErrText   string
		wantCount     int
	}{
		{
			name:          "When redirect URI is missing it should append the callback URI",
			serviceConfig: "global:\n  auth:\n    pamOidcIssuer:\n      redirectUris:\n        - http://localhost:8080/callback\n",
			callbackURI:   "http://localhost:18080/callback",
			wantCount:     1,
		},
		{
			name:          "When redirect URI already exists it should keep one callback URI",
			serviceConfig: "global:\n  auth:\n    pamOidcIssuer:\n      redirectUris:\n        - http://localhost:18080/callback\n",
			callbackURI:   "http://localhost:18080/callback",
			wantCount:     1,
		},
		{
			name:          "When PAM issuer is disabled it should return an error",
			serviceConfig: "global:\n  auth:\n    pamOidcIssuer:\n      enabled: false\n",
			callbackURI:   "http://localhost:18080/callback",
			wantErrText:   "bundled PAM issuer is disabled",
		},
		{
			name:          "When parent maps are missing it should create nested PAM issuer config",
			serviceConfig: "{}\n",
			callbackURI:   "http://localhost:18080/callback",
			wantCount:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := withQuadletPAMRedirectURI(tt.serviceConfig, tt.callbackURI)
			if tt.wantErrText != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrText)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantCount, strings.Count(got, tt.callbackURI))
			var rendered map[string]interface{}
			require.NoError(t, yaml.Unmarshal([]byte(got), &rendered))
			pamIssuer, err := ensurePAMIssuerConfig(rendered)
			require.NoError(t, err)
			require.True(t, stringSliceContains(stringSliceFromYAML(pamIssuer["redirectUris"]), tt.callbackURI))
		})
	}
}

// TestEnsurePAMIssuerConfig verifies nested PAM issuer config creation and disabled issuer handling.
func TestEnsurePAMIssuerConfig(t *testing.T) {
	config := map[string]interface{}{}

	pamIssuer, err := ensurePAMIssuerConfig(config)

	require.NoError(t, err)
	require.NotNil(t, pamIssuer)
	require.Contains(t, config, "global")

	_, err = ensurePAMIssuerConfig(map[string]interface{}{
		"global": map[string]interface{}{
			"auth": map[string]interface{}{
				"pamOidcIssuer": map[string]interface{}{
					"enabled": false,
				},
			},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "bundled PAM issuer is disabled")
}

// TestStringSliceFromYAML verifies YAML sequence conversion handles supported and ignored values.
func TestStringSliceFromYAML(t *testing.T) {
	require.Equal(t, []string{"one", "two"}, stringSliceFromYAML([]string{"one", "two"}))
	require.Equal(t, []string{"one", "2"}, stringSliceFromYAML([]interface{}{"one", nil, 2}))
	require.Nil(t, stringSliceFromYAML("one"))
}

// TestStringSliceContains verifies exact string membership checks.
func TestStringSliceContains(t *testing.T) {
	require.True(t, stringSliceContains([]string{"one", "two"}, "two"))
	require.False(t, stringSliceContains([]string{"one", "two"}, "tw"))
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

// TestResolveLoginFormAction verifies login form actions resolve to the endpoint submitted by a browser.
func TestResolveLoginFormAction(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		action  string
		want    string
	}{
		{
			name:    "When PAM authorize form has no action it should submit to PAM login endpoint",
			baseURL: "https://auth.example.com/api/v1/auth/authorize",
			want:    "https://auth.example.com/api/v1/auth/login",
		},
		{
			name:    "When a non-authorize form has no action it should submit to the same page",
			baseURL: "https://auth.example.com/login",
			want:    "https://auth.example.com/login",
		},
		{
			name:    "When form action is relative it should resolve against the form page",
			baseURL: "https://auth.example.com/realms/flightctl/protocol/openid-connect/auth",
			action:  "../login-actions/authenticate",
			want:    "https://auth.example.com/realms/flightctl/protocol/login-actions/authenticate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL, err := url.Parse(tt.baseURL)
			require.NoError(t, err)

			got, err := resolveLoginFormAction(baseURL, tt.action)

			require.NoError(t, err)
			require.Equal(t, tt.want, got.String())
		})
	}
}

// TestLoginRedirectFromBody verifies PAM JavaScript redirect responses resolve to follow-up URLs.
func TestLoginRedirectFromBody(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		body    string
		want    string
		wantOK  bool
	}{
		{
			name:    "When PAM login returns relative authorize URL it should resolve against login endpoint",
			baseURL: "https://auth.example.com/api/v1/auth/login",
			body:    "authorize?response_type=code&client_id=flightctl",
			want:    "https://auth.example.com/api/v1/auth/authorize?response_type=code&client_id=flightctl",
			wantOK:  true,
		},
		{
			name:    "When login response is not a URL it should not return a redirect",
			baseURL: "https://auth.example.com/api/v1/auth/login",
			body:    "logged in",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL, err := url.Parse(tt.baseURL)
			require.NoError(t, err)

			got, ok, err := loginRedirectFromBody(baseURL, []byte(tt.body))

			require.NoError(t, err)
			require.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				require.Equal(t, tt.want, got.String())
			}
		})
	}
}

// TestValidateLoginFormTarget verifies credentials are only submitted within the provider page origin.
func TestValidateLoginFormTarget(t *testing.T) {
	tests := []struct {
		name      string
		pageURL   string
		targetURL string
		wantErr   bool
	}{
		{
			name:      "When target has same origin it should allow submission",
			pageURL:   "https://auth.example.com/api/v1/auth/authorize",
			targetURL: "https://auth.example.com/api/v1/auth/login",
		},
		{
			name:      "When target has different host it should reject submission",
			pageURL:   "https://auth.example.com/api/v1/auth/authorize",
			targetURL: "https://other.example.com/api/v1/auth/login",
			wantErr:   true,
		},
		{
			name:      "When target has different scheme it should reject submission",
			pageURL:   "https://auth.example.com/api/v1/auth/authorize",
			targetURL: "http://auth.example.com/api/v1/auth/login",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pageURL, err := url.Parse(tt.pageURL)
			require.NoError(t, err)
			targetURL, err := url.Parse(tt.targetURL)
			require.NoError(t, err)

			err = validateLoginFormTarget(pageURL, targetURL)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
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

// ServiceExists is unused by these tests and satisfies infra.InfraProvider.
func (f fakeInfraProvider) ServiceExists(context.Context, infra.ServiceName) (bool, error) {
	return false, nil
}

var _ infra.InfraProvider = fakeInfraProvider{}
