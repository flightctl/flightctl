package login

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupOIDCDiscoveryServer creates a test HTTP server that serves a minimal OIDC discovery
// document. It records every path that was requested so tests can assert on it.
func setupOIDCDiscoveryServer(t *testing.T, validPaths map[string]bool) (server *httptest.Server, requestedPaths *[]string, mu *sync.Mutex) {
	t.Helper()
	paths := []string{}
	var m sync.Mutex

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.Lock()
		paths = append(paths, r.URL.Path)
		m.Unlock()

		if validPaths[r.URL.Path] {
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
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	return server, &paths, &m
}

// TestOIDC_getOIDCClient_DiscoveryURL verifies that getOIDCClient builds the OIDC discovery
// URL without a double slash regardless of whether the issuer has a trailing slash.
func TestOIDC_getOIDCClient_DiscoveryURL(t *testing.T) {
	scopes := []string{"openid"}
	tests := []struct {
		name         string
		issuerSuffix string
		validPath    string
	}{
		{
			name:         "When issuer has no trailing slash it should reach the correct discovery endpoint",
			issuerSuffix: "",
			validPath:    "/.well-known/openid-configuration",
		},
		{
			name:         "When issuer has a trailing slash it should reach the correct discovery endpoint without double slash",
			issuerSuffix: "/",
			validPath:    "/.well-known/openid-configuration",
		},
		{
			name:         "When issuer has a realm path with trailing slash it should reach the correct discovery endpoint",
			issuerSuffix: "/realms/myrealm/",
			validPath:    "/realms/myrealm/.well-known/openid-configuration",
		},
		{
			name:         "When issuer has a realm path without trailing slash it should reach the correct discovery endpoint",
			issuerSuffix: "/realms/myrealm",
			validPath:    "/realms/myrealm/.well-known/openid-configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, requestedPaths, mu := setupOIDCDiscoveryServer(t, map[string]bool{tt.validPath: true})

			issuer := server.URL + tt.issuerSuffix

			o := &OIDC{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("test-provider"),
				},
				Spec: api.OIDCProviderSpec{
					Issuer:   issuer,
					ClientId: "test-client",
					Scopes:   &scopes,
				},
				ApiServerURL:       server.URL,
				InsecureSkipVerify: true,
			}

			_, err := o.getOIDCClient("http://localhost:8080/callback")
			require.NoError(t, err, fmt.Sprintf("getOIDCClient should succeed when issuer is %q", issuer))

			mu.Lock()
			paths := append([]string{}, *requestedPaths...)
			mu.Unlock()

			require.NotEmpty(t, paths, "Expected at least one request to discovery server")
			for _, path := range paths {
				assert.NotContains(t, path, "//",
					"Discovery request path must not contain double slash (issuer: %q, got path: %q)", issuer, path)
			}
		})
	}
}

// TestOIDC_authPasswordFlow_DiscoveryURL verifies that authPasswordFlow builds the OIDC
// discovery URL without a double slash regardless of trailing slash in the issuer.
func TestOIDC_authPasswordFlow_DiscoveryURL(t *testing.T) {
	scopes := []string{"openid"}
	tests := []struct {
		name         string
		issuerSuffix string
		validPath    string
	}{
		{
			name:         "When issuer has no trailing slash it should reach the correct discovery endpoint",
			issuerSuffix: "",
			validPath:    "/.well-known/openid-configuration",
		},
		{
			name:         "When issuer has a trailing slash it should reach the correct discovery endpoint without double slash",
			issuerSuffix: "/",
			validPath:    "/.well-known/openid-configuration",
		},
		{
			name:         "When issuer has a realm path with trailing slash it should reach the correct discovery endpoint",
			issuerSuffix: "/realms/myrealm/",
			validPath:    "/realms/myrealm/.well-known/openid-configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The token endpoint returned by discovery will be called by the password flow,
			// so we need to serve that too (return an error from it — we only care that
			// discovery itself succeeds with the correct URL).
			validPaths := map[string]bool{
				tt.validPath: true,
			}
			server, requestedPaths, mu := setupOIDCDiscoveryServer(t, validPaths)

			issuer := server.URL + tt.issuerSuffix

			o := &OIDC{
				Spec: api.OIDCProviderSpec{
					Issuer:   issuer,
					ClientId: "test-client",
					Scopes:   &scopes,
				},
				Username:           "user",
				Password:           "pass",
				InsecureSkipVerify: true,
			}

			// authPasswordFlow will fail at the token exchange step (our mock server
			// doesn't implement the token endpoint), but we only care that the discovery
			// request itself was sent to the correct path.
			_, _ = o.authPasswordFlow()

			mu.Lock()
			paths := append([]string{}, *requestedPaths...)
			mu.Unlock()

			require.NotEmpty(t, paths, "Expected at least one request to discovery server")
			for _, path := range paths {
				assert.NotContains(t, path, "//",
					"Discovery request path must not contain double slash (issuer: %q, got path: %q)", issuer, path)
			}
		})
	}
}
