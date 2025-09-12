package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/stretchr/testify/require"
)

// Helper function to create a test JWT token with custom claims
func createTestJWT(t *testing.T, claims map[string]interface{}) string {
	// Generate RSA key for signing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Create JWK from private key
	key, err := jwk.FromRaw(privateKey)
	require.NoError(t, err)

	// Set key ID and algorithm
	err = key.Set(jwk.KeyIDKey, "test-key-id")
	require.NoError(t, err)
	err = key.Set(jwk.AlgorithmKey, jwa.RS256)
	require.NoError(t, err)

	// Build JWT token with claims
	token := jwt.New()

	// Set standard claims
	err = token.Set(jwt.IssuerKey, "https://test-oidc-provider.com")
	require.NoError(t, err)
	err = token.Set(jwt.AudienceKey, "flightctl")
	require.NoError(t, err)
	err = token.Set(jwt.ExpirationKey, time.Now().Add(time.Hour))
	require.NoError(t, err)
	err = token.Set(jwt.IssuedAtKey, time.Now())
	require.NoError(t, err)

	// Set custom claims
	for k, v := range claims {
		err = token.Set(k, v)
		require.NoError(t, err)
	}

	// Sign the token
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.RS256, key))
	require.NoError(t, err)

	return string(signed)
}

func TestJWTAuth_GetIdentity(t *testing.T) {
	tests := []struct {
		name           string
		claims         map[string]interface{}
		expectedResult *common.Identity
		expectedError  string
		description    string
	}{
		{
			name: "Standard OIDC with preferred_username",
			claims: map[string]interface{}{
				"sub":                "user123",
				"preferred_username": "testuser",
				"email":              "testuser@example.com",
				"groups":             []interface{}{"developers", "admins"},
			},
			expectedResult: &common.Identity{
				Username: "testuser",
				UID:      "user123",
				Groups:   []string{"developers", "admins"},
			},
			description: "Should extract preferred_username as primary identifier",
		},
		{
			name: "Keycloak style claims",
			claims: map[string]interface{}{
				"sub":                "f47ac10b-58cc-4372-a567-0e02b2c3d479",
				"preferred_username": "kube:admin",
				"email":              "admin@example.com",
				"groups":             []interface{}{"system:masters", "system:authenticated"},
			},
			expectedResult: &common.Identity{
				Username: "kube:admin",
				UID:      "f47ac10b-58cc-4372-a567-0e02b2c3d479",
				Groups:   []string{"system:masters", "system:authenticated"},
			},
			description: "Should handle Keycloak-style tokens",
		},
		{
			name: "Email fallback when no preferred_username",
			claims: map[string]interface{}{
				"sub":   "user456",
				"email": "user456@company.com",
				"name":  "Jane Doe",
			},
			expectedResult: &common.Identity{
				Username: "user456@company.com",
				UID:      "user456",
				Groups:   []string{},
			},
			description: "Should fallback to email when preferred_username is missing",
		},
		{
			name: "Name fallback when no username or email",
			claims: map[string]interface{}{
				"sub":  "user789",
				"name": "John Smith",
			},
			expectedResult: &common.Identity{
				Username: "John Smith",
				UID:      "user789",
				Groups:   []string{},
			},
			description: "Should fallback to name when both preferred_username and email are missing",
		},
		{
			name: "Subject fallback when only sub available",
			claims: map[string]interface{}{
				"sub": "minimal-user-123",
			},
			expectedResult: &common.Identity{
				Username: "minimal-user-123",
				UID:      "minimal-user-123",
				Groups:   []string{},
			},
			description: "Should fallback to sub as username when no other claims available",
		},
		{
			name: "Azure AD style token with roles",
			claims: map[string]interface{}{
				"sub":                "azure-user-id-123",
				"preferred_username": "user@company.onmicrosoft.com",
				"email":              "user@company.onmicrosoft.com",
				"roles":              []interface{}{"Directory.Read.All", "User.Read"},
			},
			expectedResult: &common.Identity{
				Username: "user@company.onmicrosoft.com",
				UID:      "azure-user-id-123",
				Groups:   []string{"Directory.Read.All", "User.Read"},
			},
			description: "Should handle Azure AD tokens with roles as groups",
		},
		{
			name: "Groups as space-separated string",
			claims: map[string]interface{}{
				"sub":                "user-string-groups",
				"preferred_username": "stringuser",
				"groups":             "group1 group2 group3",
			},
			expectedResult: &common.Identity{
				Username: "stringuser",
				UID:      "user-string-groups",
				Groups:   []string{"group1", "group2", "group3"},
			},
			description: "Should handle groups as space-separated string",
		},
		{
			name: "Mixed group types - filters non-strings",
			claims: map[string]interface{}{
				"sub":                "user-mixed",
				"preferred_username": "mixeduser",
				"groups":             []interface{}{"string-group", 123, true, "another-group"},
			},
			expectedResult: &common.Identity{
				Username: "mixeduser",
				UID:      "user-mixed",
				Groups:   []string{"string-group", "another-group"}, // Only string values
			},
			description: "Should filter out non-string values from groups",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test JWT token
			tokenString := createTestJWT(t, tt.claims)

			// Create JWT auth instance
			jwtAuth := authn.JWTAuth{}

			// Call the method we're testing
			result, err := jwtAuth.GetIdentity(context.Background(), tokenString)

			if tt.expectedError != "" {
				require.Error(t, err, "Expected error for test case: %s", tt.description)
				require.Contains(t, err.Error(), tt.expectedError)
				require.Nil(t, result)
			} else {
				require.NoError(t, err, "Unexpected error for test case: %s", tt.description)
				require.NotNil(t, result, "Result should not be nil for: %s", tt.description)
				require.Equal(t, tt.expectedResult.Username, result.Username, "Username mismatch for: %s", tt.description)
				require.Equal(t, tt.expectedResult.UID, result.UID, "UID mismatch for: %s", tt.description)
				require.Equal(t, tt.expectedResult.Groups, result.Groups, "Groups mismatch for: %s", tt.description)
			}
		})
	}
}

func TestJWTAuth_GetIdentity_EdgeCases(t *testing.T) {
	jwtAuth := authn.JWTAuth{}

	tests := []struct {
		name  string
		token string
	}{
		{"Empty token", ""},
		{"Whitespace-only token", "   "},
		{"Invalid token", "invalid.jwt.token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := jwtAuth.GetIdentity(context.Background(), tt.token)
			require.Error(t, err)
			require.Nil(t, result)
		})
	}
}

// Test the common utility functions used by JWT auth
func TestJWTClaimsExtractionUtilities(t *testing.T) {
	t.Run("ExtractUsernameFromJWTClaims", func(t *testing.T) {
		tests := []struct {
			name     string
			claims   map[string]interface{}
			expected string
		}{
			{"preferred_username takes priority", map[string]interface{}{"preferred_username": "user1", "email": "user2@example.com", "name": "User Three", "sub": "user4"}, "user1"},
			{"email fallback", map[string]interface{}{"email": "user@example.com", "name": "User Name", "sub": "user-id"}, "user@example.com"},
			{"name fallback", map[string]interface{}{"name": "User Name", "sub": "user-id"}, "User Name"},
			{"sub fallback", map[string]interface{}{"sub": "user-id-only"}, "user-id-only"},
			{"empty all fields", map[string]interface{}{"sub": "", "preferred_username": "", "email": "", "name": ""}, ""},
			{"no relevant fields", map[string]interface{}{"irrelevant": "value"}, ""},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := common.ExtractUsernameFromJWTClaims(tt.claims)
				require.Equal(t, tt.expected, result)
			})
		}
	})

	t.Run("ExtractGroupsFromJWTClaims", func(t *testing.T) {
		tests := []struct {
			name     string
			claims   map[string]interface{}
			expected []string
		}{
			{"groups as array", map[string]interface{}{"groups": []interface{}{"admin", "user"}}, []string{"admin", "user"}},
			{"roles as array", map[string]interface{}{"roles": []interface{}{"role1", "role2"}}, []string{"role1", "role2"}},
			{"groups as string", map[string]interface{}{"groups": "admin user guest"}, []string{"admin", "user", "guest"}},
			{"both groups and roles", map[string]interface{}{"groups": []interface{}{"admin"}, "roles": []interface{}{"role1"}}, []string{"admin", "role1"}},
			{"duplicates removed", map[string]interface{}{"groups": []interface{}{"admin", "user"}, "roles": []interface{}{"admin", "role1"}}, []string{"admin", "user", "role1"}},
			{"mixed types filtered", map[string]interface{}{"groups": []interface{}{"admin", 123, "user"}}, []string{"admin", "user"}},
			{"empty", map[string]interface{}{}, []string{}},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := common.ExtractGroupsFromJWTClaims(tt.claims)
				require.Equal(t, tt.expected, result)
			})
		}
	})
}
