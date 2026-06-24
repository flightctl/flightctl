package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestAuthTokenProxy builds a minimal AuthTokenProxy suitable for unit-testing
// discoverTokenEndpoint. Only the fields used by that method are populated.
func newTestAuthTokenProxy(discoveryClient *http.Client) *AuthTokenProxy {
	return &AuthTokenProxy{
		tokenEndpointCache:    make(map[string]*cacheEntry),
		tokenEndpointCacheTTL: 5 * time.Minute,
		discoveryClient:       discoveryClient,
	}
}

// TestAuthTokenProxy_discoverTokenEndpoint_TrailingSlash verifies that discoverTokenEndpoint
// correctly normalizes the issuer URL before building the discovery URL, so a trailing
// slash in the issuer never produces a double-slash path.
func TestAuthTokenProxy_discoverTokenEndpoint_TrailingSlash(t *testing.T) {
	tests := []struct {
		name         string
		issuerSuffix string
		validPath    string
		tokenEndpoint string
	}{
		{
			name:          "When issuer has no trailing slash it should discover the token endpoint",
			issuerSuffix:  "",
			validPath:     "/.well-known/openid-configuration",
			tokenEndpoint: "/token",
		},
		{
			name:          "When issuer has a trailing slash it should discover the token endpoint without double slash",
			issuerSuffix:  "/",
			validPath:     "/.well-known/openid-configuration",
			tokenEndpoint: "/token",
		},
		{
			name:          "When issuer has a realm path with trailing slash it should discover the token endpoint",
			issuerSuffix:  "/realms/myrealm/",
			validPath:     "/realms/myrealm/.well-known/openid-configuration",
			tokenEndpoint: "/realms/myrealm/token",
		},
		{
			name:          "When issuer has a realm path without trailing slash it should discover the token endpoint",
			issuerSuffix:  "/realms/myrealm",
			validPath:     "/realms/myrealm/.well-known/openid-configuration",
			tokenEndpoint: "/realms/myrealm/token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var requestedPaths []string

			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requestedPaths = append(requestedPaths, r.URL.Path)
				mu.Unlock()

				if r.URL.Path != tt.validPath {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				doc := struct {
					TokenEndpoint string `json:"token_endpoint"`
				}{
					TokenEndpoint: server.URL + tt.tokenEndpoint,
				}
				_ = json.NewEncoder(w).Encode(doc)
			}))
			defer server.Close()

			issuer := server.URL + tt.issuerSuffix
			proxy := newTestAuthTokenProxy(server.Client())

			endpoint, err := proxy.discoverTokenEndpoint(issuer)
			require.NoError(t, err, "discoverTokenEndpoint should succeed for issuer %q", issuer)
			assert.Equal(t, server.URL+tt.tokenEndpoint, endpoint)

			mu.Lock()
			paths := append([]string{}, requestedPaths...)
			mu.Unlock()

			require.NotEmpty(t, paths, "Expected at least one request to the discovery server")
			for _, path := range paths {
				assert.NotContains(t, path, "//",
					"Discovery request path must not contain double slash (issuer: %q, path: %q)", issuer, path)
			}
		})
	}
}

// TestAuthTokenProxy_discoverTokenEndpoint_Caching verifies that repeated calls with the
// same issuer are served from cache (only one HTTP round-trip is made).
func TestAuthTokenProxy_discoverTokenEndpoint_Caching(t *testing.T) {
	callCount := 0
	var mu sync.Mutex

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		doc := struct {
			TokenEndpoint string `json:"token_endpoint"`
		}{
			TokenEndpoint: server.URL + "/token",
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer server.Close()

	proxy := newTestAuthTokenProxy(server.Client())

	for i := 0; i < 5; i++ {
		endpoint, err := proxy.discoverTokenEndpoint(server.URL)
		require.NoError(t, err)
		assert.Equal(t, server.URL+"/token", endpoint)
	}

	mu.Lock()
	count := callCount
	mu.Unlock()
	assert.Equal(t, 1, count, "Expected exactly one HTTP call to the discovery server (cache should serve the rest)")
}

// TestAuthTokenProxy_discoverTokenEndpoint_InvalidIssuer verifies that an invalid issuer
// URL returns an error without making any HTTP requests.
func TestAuthTokenProxy_discoverTokenEndpoint_InvalidIssuer(t *testing.T) {
	tests := []struct {
		name   string
		issuer string
	}{
		{
			name:   "When issuer is empty it should return an error",
			issuer: "",
		},
		{
			name:   "When issuer has no scheme it should return an error",
			issuer: "example.com/realms/myrealm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := newTestAuthTokenProxy(&http.Client{})
			_, err := proxy.discoverTokenEndpoint(tt.issuer)
			assert.Error(t, err, "Expected error for invalid issuer %q", tt.issuer)
		})
	}
}
