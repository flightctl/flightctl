package authn

import (
	"context"
	"net/http"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
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
	issuer  string
	valid   bool
	enabled bool // whether the provider is enabled
}

func (m *MockAuthNMiddleware) GetAuthToken(r *http.Request) (string, error) {
	// Return a proper JWT token for testing
	return createTestJWT(m.issuer, "testuser", "test-client"), nil
}

func (m *MockAuthNMiddleware) IsEnabled() bool {
	return m.enabled
}

func (m *MockAuthNMiddleware) ValidateToken(ctx context.Context, token string) error {
	if !m.valid {
		return assert.AnError
	}

	// If this is an AAP provider (non-URL issuer), accept any token
	if m.issuer == "aap" {
		return nil
	}

	// For other providers, parse token to check if it matches this provider's issuer
	parsedToken, err := parseToken(token)
	if err != nil {
		// Not a valid JWT - reject it
		return assert.AnError
	}

	// Check if issuer matches
	if parsedToken.Issuer() != m.issuer {
		return assert.AnError
	}

	return nil
}

func (m *MockAuthNMiddleware) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	if !m.valid {
		return nil, assert.AnError
	}

	// If this is an AAP provider (non-URL issuer), accept any token
	if m.issuer == "aap" {
		return &common.BaseIdentity{}, nil
	}

	// For other providers, parse token to check if it matches this provider's issuer
	parsedToken, err := parseToken(token)
	if err != nil {
		// Not a valid JWT - reject it
		return nil, assert.AnError
	}

	// Check if issuer matches
	if parsedToken.Issuer() != m.issuer {
		return nil, assert.AnError
	}

	return &common.BaseIdentity{}, nil
}

func (m *MockAuthNMiddleware) GetAuthConfig() *api.AuthConfig {
	providerName := m.issuer

	// Create OIDC provider spec
	oidcSpec := api.OIDCProviderSpec{
		Issuer:       m.issuer,
		ClientId:     "test-client",
		ClientSecret: lo.ToPtr("test-secret"),
		ProviderType: api.OIDCProviderSpecProviderType("oidc"),
		Enabled:      lo.ToPtr(m.enabled),
	}

	provider := api.AuthProvider{
		ApiVersion: api.AuthProviderAPIVersion,
		Kind:       api.AuthProviderKind,
		Metadata: api.ObjectMeta{
			Name: &providerName,
		},
		Spec: api.AuthProviderSpec{},
	}
	_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)

	return &api.AuthConfig{
		ApiVersion:           api.AuthConfigAPIVersion,
		DefaultProvider:      &providerName,
		OrganizationsEnabled: lo.ToPtr(false),
		Providers:            &[]api.AuthProvider{provider},
	}
}

func TestMultiAuth_ValidateToken(t *testing.T) {
	// Create a mock store (we won't use it in this test)
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Add mock static methods
	validMethod := &MockAuthNMiddleware{issuer: "https://valid-issuer.com", valid: true, enabled: true}
	invalidMethod := &MockAuthNMiddleware{issuer: "https://invalid-issuer.com", valid: false, enabled: true}

	multiAuth.AddStaticProvider("https://valid-issuer.com:test-client", validMethod)
	multiAuth.AddStaticProvider("https://invalid-issuer.com:test-client", invalidMethod)

	// Add mock AAP provider
	aapMethod := &MockAuthNMiddleware{issuer: "aap", valid: true, enabled: true}
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
	validMethod := &MockAuthNMiddleware{issuer: "https://valid-issuer.com", valid: true, enabled: true}
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
	validMethod := &MockAuthNMiddleware{issuer: "https://valid-issuer.com", valid: true, enabled: true}
	multiAuth.AddStaticProvider("https://valid-issuer.com:test-client", validMethod)

	// Add mock AAP method
	aapMethod := &MockAuthNMiddleware{issuer: "aap", valid: true, enabled: true}
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
	assert.Equal(t, api.AuthConfigAPIVersion, config.ApiVersion)
	assert.Nil(t, config.DefaultProvider)

	// Add first static method
	mockMethod1 := &MockAuthNMiddleware{issuer: "https://test-issuer-1.com", valid: true, enabled: true}
	multiAuth.AddStaticProvider("https://test-issuer-1.com", mockMethod1)

	config = multiAuth.GetAuthConfig()
	assert.NotNil(t, config)
	assert.Equal(t, api.AuthConfigAPIVersion, config.ApiVersion)
	assert.NotNil(t, config.DefaultProvider)
	assert.Equal(t, "https://test-issuer-1.com", *config.DefaultProvider)
	assert.NotNil(t, config.Providers)
	assert.Len(t, *config.Providers, 1)
	// Verify first provider name
	assert.Equal(t, "https://test-issuer-1.com", *(*config.Providers)[0].Metadata.Name)

	// Add second static method
	mockMethod2 := &MockAuthNMiddleware{issuer: "https://test-issuer-2.com", valid: true, enabled: true}
	multiAuth.AddStaticProvider("https://test-issuer-2.com", mockMethod2)

	config = multiAuth.GetAuthConfig()
	assert.NotNil(t, config)
	assert.NotNil(t, config.Providers)
	assert.Len(t, *config.Providers, 2)
	// Verify both providers have names
	assert.NotNil(t, (*config.Providers)[0].Metadata.Name)
	assert.NotNil(t, (*config.Providers)[1].Metadata.Name)
	// Default provider should still be the first one
	assert.Equal(t, "https://test-issuer-1.com", *config.DefaultProvider)
}

func TestMultiAuth_HasMethods(t *testing.T) {
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Initially no providers
	assert.Equal(t, 0, len(multiAuth.staticProviders))

	// Add static provider
	mockMethod := &MockAuthNMiddleware{issuer: "https://test-issuer.com", valid: true, enabled: true}
	multiAuth.AddStaticProvider("https://test-issuer.com", mockMethod)
	assert.Equal(t, 1, len(multiAuth.staticProviders))
	_, hasAAP := multiAuth.staticProviders["aap"]
	assert.False(t, hasAAP)

	// Add AAP provider
	aapMethod := &MockAuthNMiddleware{issuer: "aap", valid: true, enabled: true}
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

func TestMultiAuth_GetAuthConfig_EnabledFlag(t *testing.T) {
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Add enabled provider
	enabledProvider := &MockAuthNMiddleware{
		issuer:  "https://enabled-issuer.com",
		valid:   true,
		enabled: true,
	}
	multiAuth.AddStaticProvider("https://enabled-issuer.com:test-client", enabledProvider)

	// Add disabled provider
	disabledProvider := &MockAuthNMiddleware{
		issuer:  "https://disabled-issuer.com",
		valid:   true,
		enabled: false,
	}
	multiAuth.AddStaticProvider("https://disabled-issuer.com:test-client", disabledProvider)

	// Get auth config
	config := multiAuth.GetAuthConfig()
	require.NotNil(t, config)
	require.NotNil(t, config.Providers)

	// Only enabled provider should be in the config
	assert.Len(t, *config.Providers, 1, "Only enabled providers should be in auth config")
	assert.Equal(t, "https://enabled-issuer.com", *(*config.Providers)[0].Metadata.Name)

	// Verify the enabled provider has enabled=true
	oidcSpec, err := (*config.Providers)[0].Spec.AsOIDCProviderSpec()
	require.NoError(t, err)
	require.NotNil(t, oidcSpec.Enabled)
	assert.True(t, *oidcSpec.Enabled)
}

func TestMultiAuth_GetAuthConfig_AllDisabled(t *testing.T) {
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Add only disabled providers
	disabledProvider1 := &MockAuthNMiddleware{
		issuer:  "https://disabled-issuer-1.com",
		valid:   true,
		enabled: false,
	}
	multiAuth.AddStaticProvider("https://disabled-issuer-1.com:test-client", disabledProvider1)

	disabledProvider2 := &MockAuthNMiddleware{
		issuer:  "https://disabled-issuer-2.com",
		valid:   true,
		enabled: false,
	}
	multiAuth.AddStaticProvider("https://disabled-issuer-2.com:test-client", disabledProvider2)

	// Get auth config
	config := multiAuth.GetAuthConfig()
	require.NotNil(t, config)
	require.NotNil(t, config.Providers)

	// No providers should be in the config since all are disabled
	assert.Len(t, *config.Providers, 0, "No providers should be in auth config when all are disabled")
	assert.Nil(t, config.DefaultProvider, "Default provider should be nil when all providers are disabled")
}

func TestMultiAuth_GetAuthConfig_MultipleEnabledProviders(t *testing.T) {
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Add multiple enabled providers
	for i := 1; i <= 3; i++ {
		provider := &MockAuthNMiddleware{
			issuer:  "https://enabled-issuer-" + string(rune('0'+i)) + ".com",
			valid:   true,
			enabled: true,
		}
		multiAuth.AddStaticProvider("https://enabled-issuer-"+string(rune('0'+i))+".com:test-client", provider)
	}

	// Add one disabled provider in the middle
	disabledProvider := &MockAuthNMiddleware{
		issuer:  "https://disabled-issuer.com",
		valid:   true,
		enabled: false,
	}
	multiAuth.AddStaticProvider("https://disabled-issuer.com:test-client", disabledProvider)

	// Get auth config
	config := multiAuth.GetAuthConfig()
	require.NotNil(t, config)
	require.NotNil(t, config.Providers)

	// Only enabled providers should be in the config
	assert.Len(t, *config.Providers, 3, "Only enabled providers should be in auth config")

	// Verify none of the providers is the disabled one
	for _, provider := range *config.Providers {
		assert.NotEqual(t, "https://disabled-issuer.com", *provider.Metadata.Name)
	}
}

func TestMultiAuth_ValidateToken_DisabledProvider(t *testing.T) {
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Add enabled provider
	enabledProvider := &MockAuthNMiddleware{
		issuer:  "https://enabled-issuer.com",
		valid:   true,
		enabled: true,
	}
	multiAuth.AddStaticProvider("https://enabled-issuer.com:test-client", enabledProvider)

	// Add disabled provider
	disabledProvider := &MockAuthNMiddleware{
		issuer:  "https://disabled-issuer.com",
		valid:   true,
		enabled: false,
	}
	multiAuth.AddStaticProvider("https://disabled-issuer.com:test-client", disabledProvider)

	ctx := context.Background()

	// Token from enabled provider should validate successfully
	enabledToken := createTestJWT("https://enabled-issuer.com", "user123", "test-client")
	err := multiAuth.ValidateToken(ctx, enabledToken)
	assert.NoError(t, err, "Token from enabled provider should validate successfully")

	// Token from disabled provider should fail validation
	disabledToken := createTestJWT("https://disabled-issuer.com", "user123", "test-client")
	err = multiAuth.ValidateToken(ctx, disabledToken)
	assert.Error(t, err, "Token from disabled provider should fail validation")
	assert.Contains(t, err.Error(), "no enabled OIDC provider found", "Error should indicate no enabled provider found")
}

func TestMultiAuth_GetPossibleProviders_EnabledFlag(t *testing.T) {
	log := logrus.New()
	multiAuth := NewMultiAuth(nil, nil, log)

	// Add enabled OIDC provider
	enabledProvider := &MockAuthNMiddleware{
		issuer:  "https://enabled-issuer.com",
		valid:   true,
		enabled: true,
	}
	multiAuth.AddStaticProvider("https://enabled-issuer.com:test-client", enabledProvider)

	// Add disabled OIDC provider
	disabledProvider := &MockAuthNMiddleware{
		issuer:  "https://disabled-issuer.com",
		valid:   true,
		enabled: false,
	}
	multiAuth.AddStaticProvider("https://disabled-issuer.com:test-client", disabledProvider)

	// Add enabled AAP provider (opaque tokens)
	enabledAAPProvider := &MockAuthNMiddleware{
		issuer:  "aap",
		valid:   true,
		enabled: true,
	}
	multiAuth.AddStaticProvider("aap", enabledAAPProvider)

	// Test with JWT token from enabled provider
	enabledToken := createTestJWT("https://enabled-issuer.com", "user123", "test-client")
	providers, _, err := multiAuth.getPossibleProviders(enabledToken)
	assert.NoError(t, err)
	assert.Len(t, providers, 1, "Should return only the enabled provider")

	// Test with JWT token from disabled provider
	disabledToken := createTestJWT("https://disabled-issuer.com", "user123", "test-client")
	_, _, err = multiAuth.getPossibleProviders(disabledToken)
	assert.Error(t, err, "Should fail to find enabled provider for disabled issuer")
	assert.Contains(t, err.Error(), "no enabled OIDC provider found")

	// Test with opaque token (should return all enabled providers since issuer is unknown)
	opaqueToken := "opaque-token-string" // #nosec G101 -- This is a test token, not a credential
	providers, _, err = multiAuth.getPossibleProviders(opaqueToken)
	assert.NoError(t, err)
	assert.Len(t, providers, 2, "Should return all enabled providers for opaque token (enabled OIDC + enabled AAP)")
}
