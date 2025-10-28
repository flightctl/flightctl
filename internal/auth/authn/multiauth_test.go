package authn

import (
	"context"
	"net/http"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestJWT creates a test JWT token with the specified claims
func createTestJWT(issuer, subject, audience string) string {
	token, _ := jwt.NewBuilder().
		Issuer(issuer).
		Subject(subject).
		Audience([]string{audience}).
		Expiration(time.Now().Add(time.Hour)).
		IssuedAt(time.Now()).
		Build()

	tokenString, _ := jwt.Sign(token, jwt.WithKey(jwa.HS256, []byte("test-secret")))
	return string(tokenString)
}

// MockAuthNMiddleware is a mock implementation of AuthNMiddleware for testing
type MockAuthNMiddleware struct {
	issuer string
	valid  bool
}

func (m *MockAuthNMiddleware) GetAuthToken(r *http.Request) (string, error) {
	// Return a proper JWT token for testing
	return createTestJWT(m.issuer, "testuser", "test-client"), nil
}

func (m *MockAuthNMiddleware) ValidateToken(ctx context.Context, token string) error {
	if m.valid {
		return nil
	}
	return assert.AnError
}

func (m *MockAuthNMiddleware) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	if m.valid {
		return &common.BaseIdentity{}, nil
	}
	return nil, assert.AnError
}

func (m *MockAuthNMiddleware) GetAuthConfig() *api.AuthConfig {
	providerType := string(api.AuthProviderInfoTypeOidc)
	provider := api.AuthProviderInfo{
		Type:      (*api.AuthProviderInfoType)(&providerType),
		AuthUrl:   &m.issuer,
		IsDefault: lo.ToPtr(true),
		IsStatic:  lo.ToPtr(true),
	}
	return &api.AuthConfig{
		DefaultProvider:      &providerType,
		OrganizationsEnabled: lo.ToPtr(false),
		Providers:            &[]api.AuthProviderInfo{provider},
	}
}

func TestMultiAuth_ValidateToken(t *testing.T) {
	// Create a mock store (we won't use it in this test)
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Add mock static methods
	validMethod := &MockAuthNMiddleware{issuer: "https://valid-issuer.com", valid: true}
	invalidMethod := &MockAuthNMiddleware{issuer: "https://invalid-issuer.com", valid: false}

	multiAuth.AddStaticProvider("https://valid-issuer.com:test-client", validMethod)
	multiAuth.AddStaticProvider("https://invalid-issuer.com:test-client", invalidMethod)

	// Add mock AAP provider
	aapMethod := &MockAuthNMiddleware{issuer: "aap", valid: true}
	multiAuth.AddStaticProvider("aap", aapMethod)

	tests := []struct {
		name        string
		token       string
		expectError bool
		description string
	}{
		{
			name:        "valid JWT token with valid issuer",
			token:       createTestJWT("https://valid-issuer.com", "user123", "test-client"),
			expectError: false,
			description: "Should validate successfully against valid issuer",
		},
		{
			name:        "valid JWT token with invalid issuer",
			token:       createTestJWT("https://invalid-issuer.com", "user123", "test-client"),
			expectError: true,
			description: "Should fail validation against invalid issuer",
		},
		{
			name:        "JWT token with unknown issuer",
			token:       createTestJWT("https://unknown-issuer.com", "user123", "test-client"),
			expectError: true,
			description: "Should fail validation against unknown issuer",
		},
		{
			name:        "invalid JWT token",
			token:       "invalid.jwt.token",
			expectError: false, // This falls back to AAP method and succeeds
			description: "Should fall back to AAP method for invalid JWT",
		},
		{
			name:        "non-JWT token (should try AAP)",
			token:       "opaque-aap-token",
			expectError: false,
			description: "Should validate successfully against AAP method",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := multiAuth.ValidateToken(ctx, tt.token)

			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
			}
		})
	}
}

func TestMultiAuth_ValidateToken_NoAAP(t *testing.T) {
	// Create a mock store (we won't use it in this test)
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Add mock static methods only (no AAP method)
	validMethod := &MockAuthNMiddleware{issuer: "https://valid-issuer.com", valid: true}
	multiAuth.AddStaticProvider("https://valid-issuer.com:test-client", validMethod)

	tests := []struct {
		name        string
		token       string
		expectError bool
		description string
	}{
		{
			name:        "valid JWT token with valid issuer",
			token:       createTestJWT("https://valid-issuer.com", "user123", "test-client"),
			expectError: false,
			description: "Should validate successfully against valid issuer",
		},
		{
			name:        "invalid JWT token without AAP method",
			token:       "invalid.jwt.token",
			expectError: true,
			description: "Should fail validation for invalid JWT when no AAP method",
		},
		{
			name:        "non-JWT token without AAP method",
			token:       "opaque-token",
			expectError: true,
			description: "Should fail validation for non-JWT when no AAP method",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := multiAuth.ValidateToken(ctx, tt.token)

			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
			}
		})
	}
}

func TestMultiAuth_GetIdentity(t *testing.T) {
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Add mock static method
	validMethod := &MockAuthNMiddleware{issuer: "https://valid-issuer.com", valid: true}
	multiAuth.AddStaticProvider("https://valid-issuer.com:test-client", validMethod)

	// Add mock AAP method
	aapMethod := &MockAuthNMiddleware{issuer: "aap", valid: true}
	multiAuth.AddStaticProvider("aap", aapMethod)

	tests := []struct {
		name        string
		token       string
		expectError bool
		description string
	}{
		{
			name:        "valid JWT token",
			token:       createTestJWT("https://valid-issuer.com", "user123", "test-client"),
			expectError: false,
			description: "Should get identity successfully",
		},
		{
			name:        "non-JWT token (should try AAP)",
			token:       "opaque-aap-token",
			expectError: false,
			description: "Should get identity from AAP method",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			identity, err := multiAuth.GetIdentity(ctx, tt.token)

			if tt.expectError {
				assert.Error(t, err, tt.description)
				assert.Nil(t, identity)
			} else {
				assert.NoError(t, err, tt.description)
				assert.NotNil(t, identity)
			}
		})
	}
}

func TestMultiAuth_GetAuthToken(t *testing.T) {
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Create a mock request with Bearer token
	req, err := http.NewRequest("GET", "/test", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-token")

	token, err := multiAuth.GetAuthToken(req)
	assert.NoError(t, err)
	assert.Equal(t, "test-token", token)
}

func TestMultiAuth_GetAuthConfig(t *testing.T) {
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Test with no methods configured
	config := multiAuth.GetAuthConfig()
	assert.NotNil(t, config)
	assert.NotNil(t, config.DefaultProvider)
	assert.Equal(t, string(api.AuthProviderInfoTypeOidc), *config.DefaultProvider)

	// Add first static method
	mockMethod1 := &MockAuthNMiddleware{issuer: "https://test-issuer-1.com", valid: true}
	multiAuth.AddStaticProvider("https://test-issuer-1.com", mockMethod1)

	config = multiAuth.GetAuthConfig()
	assert.NotNil(t, config)
	assert.NotNil(t, config.DefaultProvider)
	assert.Equal(t, string(api.AuthProviderInfoTypeOidc), *config.DefaultProvider)
	assert.NotNil(t, config.Providers)
	assert.Len(t, *config.Providers, 1)
	// Verify first provider is default and static
	assert.Equal(t, "https://test-issuer-1.com", *(*config.Providers)[0].AuthUrl)
	assert.True(t, *(*config.Providers)[0].IsDefault)
	assert.True(t, *(*config.Providers)[0].IsStatic)

	// Add second static method
	mockMethod2 := &MockAuthNMiddleware{issuer: "https://test-issuer-2.com", valid: true}
	multiAuth.AddStaticProvider("https://test-issuer-2.com", mockMethod2)

	config = multiAuth.GetAuthConfig()
	assert.NotNil(t, config)
	assert.NotNil(t, config.Providers)
	assert.Len(t, *config.Providers, 2)
	// Verify first provider is default
	assert.True(t, *(*config.Providers)[0].IsDefault)
	assert.True(t, *(*config.Providers)[0].IsStatic)
	// Verify second provider is not default but is static
	assert.False(t, *(*config.Providers)[1].IsDefault)
	assert.True(t, *(*config.Providers)[1].IsStatic)
}

func TestMultiAuth_HasMethods(t *testing.T) {
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Initially no providers
	assert.Equal(t, 0, len(multiAuth.staticProviders))

	// Add static provider
	mockMethod := &MockAuthNMiddleware{issuer: "https://test-issuer.com", valid: true}
	multiAuth.AddStaticProvider("https://test-issuer.com", mockMethod)
	assert.Equal(t, 1, len(multiAuth.staticProviders))
	_, hasAAP := multiAuth.staticProviders["aap"]
	assert.False(t, hasAAP)

	// Add AAP provider
	aapMethod := &MockAuthNMiddleware{issuer: "aap", valid: true}
	multiAuth.AddStaticProvider("aap", aapMethod)
	assert.Equal(t, 2, len(multiAuth.staticProviders))
	_, hasAAP = multiAuth.staticProviders["aap"]
	assert.True(t, hasAAP)
}

func TestExtractIssuerFromToken(t *testing.T) {
	tests := []struct {
		name        string
		token       string
		expectedIss string
		expectError bool
	}{
		{
			name:        "valid JWT with issuer",
			token:       `{"iss":"https://example.com","sub":"user123"}`,
			expectedIss: "https://example.com",
			expectError: false,
		},
		{
			name:        "JWT without issuer",
			token:       `{"sub":"user123"}`,
			expectedIss: "",
			expectError: true,
		},
		{
			name:        "invalid JWT",
			token:       "invalid.jwt.token",
			expectedIss: "",
			expectError: true,
		},
		{
			name:        "non-JWT string",
			token:       "opaque-token",
			expectedIss: "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsedToken, err := parseToken(tt.token)

			if tt.expectError {
				// For cases where we expect an error, either parsing should fail
				// or the issuer should be empty (which we treat as an error condition)
				if err != nil {
					assert.Error(t, err)
				} else {
					issuer := parsedToken.Issuer()
					assert.Empty(t, issuer, "Expected empty issuer for error case")
				}
			} else {
				assert.NoError(t, err)
				issuer := parsedToken.Issuer()
				assert.Equal(t, tt.expectedIss, issuer)
			}
		})
	}
}
