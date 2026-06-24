package login

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTokenProxyServer creates a test server that records every path requested.
// It always returns 200 so that client creation can complete past the token proxy step.
func setupTokenProxyServer(t *testing.T) (server *httptest.Server, requestedPaths *[]string, mu *sync.Mutex) {
	t.Helper()
	paths := []string{}
	var m sync.Mutex
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.Lock()
		paths = append(paths, r.URL.Path)
		m.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, &paths, &m
}

// assertNoDoubleSlash asserts that none of the captured request paths contain "//".
func assertNoDoubleSlash(t *testing.T, paths []string, label string) {
	t.Helper()
	for _, p := range paths {
		assert.NotContains(t, p, "//", "%s: request path must not contain double slash, got: %q", label, p)
	}
}

// ---------------------------------------------------------------------------
// OAuth2
// ---------------------------------------------------------------------------

// TestOAuth2_getOAuth2Client_TokenProxyURL verifies that getOAuth2Client builds the
// token-proxy URL without a double slash regardless of trailing slash in the API server URL.
func TestOAuth2_getOAuth2Client_TokenProxyURL(t *testing.T) {
	scopes := []string{"openid"}
	tests := []struct {
		name            string
		apiServerSuffix string
	}{
		{
			name:            "When API server URL has no trailing slash it should produce the correct token proxy URL",
			apiServerSuffix: "",
		},
		{
			name:            "When API server URL has a trailing slash it should produce the correct token proxy URL without double slash",
			apiServerSuffix: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, requestedPaths, mu := setupTokenProxyServer(t)
			apiServerURL := server.URL + tt.apiServerSuffix

			o := &OAuth2{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("test-oauth2"),
				},
				Spec: api.OAuth2ProviderSpec{
					AuthorizationUrl: server.URL + "/authorize",
					TokenUrl:         server.URL + "/token",
					UserinfoUrl:      server.URL + "/userinfo",
					ClientId:         "test-client",
					Scopes:           &scopes,
				},
				ApiServerURL:       apiServerURL,
				InsecureSkipVerify: true,
			}

			_, err := o.getOAuth2Client("http://localhost:8080/callback")
			require.NoError(t, err, "getOAuth2Client should succeed for API server URL %q", apiServerURL)

			mu.Lock()
			paths := append([]string{}, *requestedPaths...)
			mu.Unlock()

			assertNoDoubleSlash(t, paths, "OAuth2 token proxy URL")
		})
	}
}

// ---------------------------------------------------------------------------
// OpenShift
// ---------------------------------------------------------------------------

// TestOpenShift_getOAuth2Client_TokenProxyURL verifies that the OpenShift provider builds the
// token-proxy URL without a double slash regardless of trailing slash in the API server URL.
func TestOpenShift_getOAuth2Client_TokenProxyURL(t *testing.T) {
	tests := []struct {
		name            string
		apiServerSuffix string
	}{
		{
			name:            "When API server URL has no trailing slash it should produce the correct token proxy URL",
			apiServerSuffix: "",
		},
		{
			name:            "When API server URL has a trailing slash it should produce the correct token proxy URL without double slash",
			apiServerSuffix: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, requestedPaths, mu := setupTokenProxyServer(t)
			apiServerURL := server.URL + tt.apiServerSuffix

			o := &OpenShift{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("test-openshift"),
				},
				Spec: api.OpenShiftProviderSpec{
					AuthorizationUrl: lo.ToPtr(server.URL + "/oauth/authorize"),
					ClientId:         lo.ToPtr("test-client"),
				},
				ApiServerURL:       apiServerURL,
				InsecureSkipVerify: true,
			}

			_, err := o.getOAuth2Client("http://localhost:8080/callback")
			require.NoError(t, err, "getOAuth2Client should succeed for API server URL %q", apiServerURL)

			mu.Lock()
			paths := append([]string{}, *requestedPaths...)
			mu.Unlock()

			assertNoDoubleSlash(t, paths, "OpenShift token proxy URL")
		})
	}
}

// ---------------------------------------------------------------------------
// AAP
// ---------------------------------------------------------------------------

// TestAAPOAuth_getOAuth2Client_TokenProxyURL verifies that the AAP provider builds the
// token-proxy URL without a double slash regardless of trailing slash in the API server URL.
func TestAAPOAuth_getOAuth2Client_TokenProxyURL(t *testing.T) {
	tests := []struct {
		name            string
		apiServerSuffix string
	}{
		{
			name:            "When API server URL has no trailing slash it should produce the correct token proxy URL",
			apiServerSuffix: "",
		},
		{
			name:            "When API server URL has a trailing slash it should produce the correct token proxy URL without double slash",
			apiServerSuffix: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, requestedPaths, mu := setupTokenProxyServer(t)
			apiServerURL := server.URL + tt.apiServerSuffix

			o := &AAPOAuth{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("test-aap"),
				},
				Spec: api.AapProviderSpec{
					AuthorizationUrl: server.URL + "/o/authorize",
					TokenUrl:         server.URL + "/o/token",
					ClientId:         "test-client",
					Scopes:           []string{"read"},
				},
				ApiServerURL:       apiServerURL,
				InsecureSkipVerify: true,
			}

			_, err := o.getOAuth2Client("http://localhost:8080/callback")
			require.NoError(t, err, "getOAuth2Client should succeed for API server URL %q", apiServerURL)

			mu.Lock()
			paths := append([]string{}, *requestedPaths...)
			mu.Unlock()

			assertNoDoubleSlash(t, paths, "AAP token proxy URL")
		})
	}
}

// ---------------------------------------------------------------------------
// OIDC — token proxy URL (complement to the discovery URL tests in oidc_test.go)
// ---------------------------------------------------------------------------

// TestOIDC_getOIDCClient_TokenProxyURL verifies that the OIDC provider builds the
// token-proxy URL without a double slash regardless of trailing slash in the API server URL.
func TestOIDC_getOIDCClient_TokenProxyURL(t *testing.T) {
	scopes := []string{"openid"}
	tests := []struct {
		name            string
		apiServerSuffix string
	}{
		{
			name:            "When API server URL has no trailing slash it should produce the correct token proxy URL",
			apiServerSuffix: "",
		},
		{
			name:            "When API server URL has a trailing slash it should produce the correct token proxy URL without double slash",
			apiServerSuffix: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We need both: a valid OIDC discovery endpoint AND a token proxy endpoint.
			// Use one server for both, serving the discovery doc on /.well-known/openid-configuration.
			var mu sync.Mutex
			var requestedPaths []string

			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requestedPaths = append(requestedPaths, r.URL.Path)
				mu.Unlock()

				if r.URL.Path == "/.well-known/openid-configuration" {
					w.Header().Set("Content-Type", "application/json")
					discovery := OIDCDiscoveryResponse{
						Issuer:                           server.URL,
						AuthorizationEndpoint:            server.URL + "/authorize",
						TokenEndpoint:                    server.URL + "/token",
						JwksUri:                          server.URL + "/jwks",
						SubjectTypesSupported:            []string{"public"},
						ResponseTypesSupported:           []string{"code"},
						IdTokenSigningAlgValuesSupported: []string{"RS256"},
					}
					_ = json.NewEncoder(w).Encode(discovery)
				} else {
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			apiServerURL := server.URL + tt.apiServerSuffix

			o := &OIDC{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("test-provider"),
				},
				Spec: api.OIDCProviderSpec{
					Issuer:   server.URL,
					ClientId: "test-client",
					Scopes:   &scopes,
				},
				ApiServerURL:       apiServerURL,
				InsecureSkipVerify: true,
			}

			_, err := o.getOIDCClient("http://localhost:8080/callback")
			require.NoError(t, err, "getOIDCClient should succeed for API server URL %q", apiServerURL)

			mu.Lock()
			paths := append([]string{}, requestedPaths...)
			mu.Unlock()

			assertNoDoubleSlash(t, paths, "OIDC token proxy URL")
		})
	}
}
