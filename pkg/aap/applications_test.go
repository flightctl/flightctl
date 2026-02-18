package aap

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateOAuthApplication(t *testing.T) {
	testCases := []struct {
		name           string
		request        *AAPOAuthApplicationRequest
		serverResponse *AAPOAuthApplicationResponse
		serverStatus   int
		expectError    bool
		errorContains  string
	}{
		{
			name: "successful creation",
			request: &AAPOAuthApplicationRequest{
				Name:                   "Flight Control",
				Organization:           1,
				AuthorizationGrantType: "authorization-code",
				ClientType:             "public",
				RedirectURIs:           "https://example.com:443/callback http://127.0.0.1/callback",
				AppURL:                 "https://example.com:443",
			},
			serverResponse: &AAPOAuthApplicationResponse{
				ID:                     123,
				Name:                   "Flight Control",
				ClientID:               "test-client-id-12345",
				ClientType:             "public",
				AuthorizationGrantType: "authorization-code",
				RedirectURIs:           "https://example.com:443/callback http://127.0.0.1/callback",
				AppURL:                 "https://example.com:443",
				Organization:           1,
			},
			serverStatus: http.StatusCreated,
			expectError:  false,
		},
		{
			name: "server returns forbidden",
			request: &AAPOAuthApplicationRequest{
				Name:                   "Flight Control",
				Organization:           1,
				AuthorizationGrantType: "authorization-code",
				ClientType:             "public",
				RedirectURIs:           "https://example.com:443/callback",
				AppURL:                 "https://example.com:443",
			},
			serverStatus:  http.StatusForbidden,
			expectError:   true,
			errorContains: "forbidden",
		},
		{
			name: "server returns error",
			request: &AAPOAuthApplicationRequest{
				Name:                   "Flight Control",
				Organization:           1,
				AuthorizationGrantType: "authorization-code",
				ClientType:             "public",
				RedirectURIs:           "https://example.com:443/callback",
				AppURL:                 "https://example.com:443",
			},
			serverStatus:  http.StatusInternalServerError,
			expectError:   true,
			errorContains: "unexpected status code: 500",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/gateway/v1/applications/", r.URL.Path)
				assert.Equal(t, "POST", r.Method)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")

				w.WriteHeader(tc.serverStatus)
				if tc.serverResponse != nil {
					respBytes, _ := json.Marshal(tc.serverResponse)
					_, _ = w.Write(respBytes)
				}
			}))
			defer server.Close()

			client, err := NewAAPGatewayClient(AAPGatewayClientOptions{
				GatewayUrl:      server.URL,
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			})
			require.NoError(t, err)

			resp, err := client.CreateOAuthApplication(context.Background(), "test-token", tc.request)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
				assert.Equal(t, tc.serverResponse.ClientID, resp.ClientID)
				assert.Equal(t, tc.serverResponse.Name, resp.Name)
				assert.Equal(t, tc.serverResponse.Organization, resp.Organization)
			}
		})
	}
}
