package authn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJWTAuth_parseAndCreateIdentity(t *testing.T) {
	// Create a test JWKS server
	testKey, err := jwk.FromRaw([]byte("test-secret-key-that-is-at-least-32-bytes-long"))
	require.NoError(t, err)
	require.NoError(t, testKey.Set(jwk.KeyIDKey, "test-key-id"))
	require.NoError(t, testKey.Set(jwk.AlgorithmKey, jwa.HS256))

	testKeySet := jwk.NewSet()
	require.NoError(t, testKeySet.AddKey(testKey))

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwksBytes, _ := json.Marshal(testKeySet)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write(jwksBytes)
		if err != nil {
			http.Error(w, "Failed to write response", http.StatusInternalServerError)
		}
	}))
	defer jwksServer.Close()

	// Create a valid test token with claims
	validToken := jwt.New()
	require.NoError(t, validToken.Set("sub", "test-user-id"))
	require.NoError(t, validToken.Set("preferred_username", "testuser"))
	require.NoError(t, validToken.Set("exp", time.Now().Add(time.Hour).Unix()))
	require.NoError(t, validToken.Set("iat", time.Now().Unix()))
	validTokenBytes, err := jwt.Sign(validToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	// Create a token with wrong type claims
	wrongTypeToken := jwt.New()
	require.NoError(t, wrongTypeToken.Set("sub", "test-user-id"))
	require.NoError(t, wrongTypeToken.Set("preferred_username", []string{"array", "instead", "of", "string"}))
	require.NoError(t, wrongTypeToken.Set("exp", time.Now().Add(time.Hour).Unix()))
	require.NoError(t, wrongTypeToken.Set("iat", time.Now().Unix()))
	wrongTypeTokenBytes, err := jwt.Sign(wrongTypeToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	// Create existing identity for context test
	existingIdentity := &JWTIdentity{
		BaseIdentity: common.BaseIdentity{},
	}
	existingIdentity.SetUID("existing-user")
	existingIdentity.SetUsername("existing-username")

	tests := []struct {
		name             string
		ctx              context.Context
		uri              string
		token            string
		expectError      bool
		expectedUID      string
		expectedUsername string
		errorContains    string
	}{
		{
			name:             "existing identity in context",
			ctx:              context.WithValue(context.Background(), consts.IdentityCtxKey, existingIdentity),
			uri:              jwksServer.URL,
			token:            string(validTokenBytes),
			expectError:      false,
			expectedUID:      "existing-user",
			expectedUsername: "existing-username",
		},
		{
			name:          "jwk fetch failure",
			ctx:           context.Background(),
			uri:           "http://invalid-url-that-does-not-exist",
			token:         string(validTokenBytes),
			expectError:   true,
			errorContains: "failed to fetch JWK set",
		},
		{
			name:          "invalid token",
			ctx:           context.Background(),
			uri:           jwksServer.URL,
			token:         "invalid.jwt.token",
			expectError:   true,
			errorContains: "failed to parse JWT token",
		},
		{
			name:             "valid token with both claims",
			ctx:              context.Background(),
			uri:              jwksServer.URL,
			token:            string(validTokenBytes),
			expectError:      false,
			expectedUID:      "test-user-id",
			expectedUsername: "testuser",
		},
		{
			name:             "valid token with wrong type claims",
			ctx:              context.Background(),
			uri:              jwksServer.URL,
			token:            string(wrongTypeTokenBytes),
			expectError:      false,
			expectedUID:      "test-user-id",
			expectedUsername: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jwtAuth := JWTAuth{
				jwksUri: tt.uri,
				client:  &http.Client{},
			}

			identity, err := jwtAuth.parseAndCreateIdentity(tt.ctx, tt.token)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, identity)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, identity)
				assert.Equal(t, tt.expectedUID, identity.GetUID())
				assert.Equal(t, tt.expectedUsername, identity.GetUsername())

				// Ensure the parsed token is set (except for existing identity case)
				if tt.name != "existing identity in context" {
					assert.NotNil(t, identity.parsedToken)
				}
			}
		})
	}
}
