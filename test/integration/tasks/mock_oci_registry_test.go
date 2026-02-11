package tasks_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// MockOciRegistry creates a mock OCI registry server for testing.
// It supports both authenticated and anonymous access patterns.
type MockOciRegistry struct {
	// RequireAuth if true, requires authentication to access /v2/
	RequireAuth bool
	// ValidUsername expected username for authenticated requests
	ValidUsername string
	// ValidPassword expected password for authenticated requests
	ValidPassword string
	// AuthServerURL the base URL of the mock server (set after creating httptest.Server)
	AuthServerURL string
	// ServiceName the service name returned in www-authenticate header
	ServiceName string
	// AuthToken token returned for authenticated requests
	AuthToken string
	// AnonymousToken token returned for anonymous requests with scope
	AnonymousToken string
	// ReturnTokenName the JSON key for the token ("token" or "access_token")
	ReturnTokenName string
}

// Handler returns an http.Handler that implements the OCI Distribution Spec v2 auth flow.
func (m *MockOciRegistry) Handler() http.Handler {
	mux := http.NewServeMux()

	// /v2/ endpoint - registry API base
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if !m.RequireAuth {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Check for bearer token
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token := strings.TrimPrefix(authHeader, "Bearer ")
			// Accept either auth token or anonymous token
			if token == m.AuthToken || token == m.AnonymousToken {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Return 401 with www-authenticate header
		serviceName := m.ServiceName
		if serviceName == "" {
			serviceName = "test-registry"
		}
		w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/v2/auth",service="%s"`, m.AuthServerURL, serviceName))
		w.WriteHeader(http.StatusUnauthorized)
	})

	// /v2/auth endpoint - token exchange
	mux.HandleFunc("/v2/auth", func(w http.ResponseWriter, r *http.Request) {
		tokenKey := m.ReturnTokenName
		if tokenKey == "" {
			tokenKey = "token"
		}

		// Check if credentials were provided
		username, password, hasBasicAuth := r.BasicAuth()

		if hasBasicAuth {
			// Authenticated request - validate credentials
			if username != m.ValidUsername || password != m.ValidPassword {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// Return auth token
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]string{tokenKey: m.AuthToken}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}

		// Anonymous request - check if scope was provided
		scope := r.URL.Query().Get("scope")
		if scope != "" && m.AnonymousToken != "" {
			// Return anonymous token
			resp := map[string]string{tokenKey: m.AnonymousToken}
			data, err := json.Marshal(resp)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}

		// No valid auth pattern
		w.WriteHeader(http.StatusUnauthorized)
	})

	return mux
}
