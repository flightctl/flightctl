//go:build linux

package issuer

import (
	"context"
	"errors"
	"os/user"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testUsername = "testuser"
	testPassword = "testpass"
)

// MockPAMAuthenticator is a simple mock implementation of PAMAuthenticator for testing
type MockPAMAuthenticator struct {
	authenticateFunc  func(username, password string) error
	lookupUserFunc    func(username string) (*user.User, error)
	getUserGroupsFunc func(systemUser *user.User) ([]string, error)
	closeFunc         func() error
}

func (m *MockPAMAuthenticator) Authenticate(username, password string) error {
	if m.authenticateFunc != nil {
		return m.authenticateFunc(username, password)
	}
	return nil
}

func (m *MockPAMAuthenticator) LookupUser(username string) (*user.User, error) {
	if m.lookupUserFunc != nil {
		return m.lookupUserFunc(username)
	}
	return nil, errors.New("not implemented")
}

func (m *MockPAMAuthenticator) GetUserGroups(systemUser *user.User) ([]string, error) {
	if m.getUserGroupsFunc != nil {
		return m.getUserGroupsFunc(systemUser)
	}
	return nil, errors.New("not implemented")
}

func (m *MockPAMAuthenticator) Close() error {
	if m.closeFunc != nil {
		return m.closeFunc()
	}
	return nil
}

// NewMockPAMAuthenticator creates a mock PAM authenticator with the specified behavior
func NewMockPAMAuthenticator(authenticateFunc func(username, password string) error, mockUser *user.User, groups []string) PAMAuthenticator {
	return &MockPAMAuthenticator{
		authenticateFunc: authenticateFunc,
		lookupUserFunc: func(username string) (*user.User, error) {
			return mockUser, nil
		},
		getUserGroupsFunc: func(systemUser *user.User) ([]string, error) {
			return groups, nil
		},
		closeFunc: func() error {
			return nil
		},
	}
}

// Helper function to create a test CA client
func createTestCAClient(t *testing.T) *fccrypto.CAClient {
	t.Helper()
	cfg := ca.NewDefault(t.TempDir())
	caClient, _, err := fccrypto.EnsureCA(cfg)
	require.NoError(t, err)
	return caClient
}

func TestNewPAMOIDCProvider(t *testing.T) {
	caClient := createTestCAClient(t)

	tests := []struct {
		name        string
		caClient    *fccrypto.CAClient
		cfg         *config.PAMOIDCIssuer
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid config",
			caClient:    caClient,
			cfg:         &config.PAMOIDCIssuer{},
			expectError: false,
		},
		{
			name:        "nil config",
			caClient:    caClient,
			cfg:         nil,
			expectError: false, // PAM doesn't require specific config like PAM service
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewPAMOIDCProvider(tt.caClient, tt.cfg)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, provider)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, provider)
				assert.NotNil(t, provider.jwtGenerator)
				assert.Equal(t, tt.cfg, provider.config)
			}
		})
	}
}

func TestPAMOIDCProvider_Token(t *testing.T) {
	// Create a minimal provider for testing
	caClient := createTestCAClient(t)
	cfg := &config.PAMOIDCIssuer{}
	provider, err := NewPAMOIDCProvider(caClient, cfg)
	require.NoError(t, err)

	tests := []struct {
		name        string
		request     *v1alpha1.TokenRequest
		expectError bool
		errorCode   string
	}{
		{
			name: "unsupported grant type",
			request: &v1alpha1.TokenRequest{
				GrantType: "unsupported",
			},
			expectError: true,
			errorCode:   "unsupported_grant_type",
		},
		{
			name: "authorization_code grant type - missing code",
			request: &v1alpha1.TokenRequest{
				GrantType: v1alpha1.AuthorizationCode,
				ClientId:  lo.ToPtr("test-client"),
			},
			expectError: true,
			errorCode:   "invalid_request",
		},
		{
			name: "authorization_code grant type - missing client credentials",
			request: &v1alpha1.TokenRequest{
				GrantType: v1alpha1.AuthorizationCode,
				Code:      lo.ToPtr("auth-code-123"),
			},
			expectError: true,
			errorCode:   "invalid_client",
		},
		{
			name: "authorization_code grant type - empty code",
			request: &v1alpha1.TokenRequest{
				GrantType: v1alpha1.AuthorizationCode,
				Code:      lo.ToPtr(""),
				ClientId:  lo.ToPtr("test-client"),
			},
			expectError: true,
			errorCode:   "invalid_request",
		},
		{
			name: "authorization_code grant type - missing client ID",
			request: &v1alpha1.TokenRequest{
				GrantType: v1alpha1.AuthorizationCode,
				Code:      lo.ToPtr("auth-code-123"),
			},
			expectError: true,
			errorCode:   "invalid_client",
		},
		{
			name: "refresh token grant type - missing refresh token",
			request: &v1alpha1.TokenRequest{
				GrantType: v1alpha1.RefreshToken,
			},
			expectError: true,
			errorCode:   "invalid_request",
		},
		{
			name: "refresh token grant type - empty refresh token",
			request: &v1alpha1.TokenRequest{
				GrantType:    v1alpha1.RefreshToken,
				RefreshToken: lo.ToPtr(""),
			},
			expectError: true,
			errorCode:   "invalid_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := provider.Token(context.Background(), tt.request)

			if tt.expectError {
				require.NoError(t, err)
				assert.NotNil(t, response.Error)
				assert.Equal(t, tt.errorCode, *response.Error)
			} else {
				// For successful cases, we'd need to mock the dependencies
				// This is a basic structure test
				assert.NotNil(t, response)
			}
		})
	}
}

func TestPAMOIDCProvider_GetOpenIDConfiguration(t *testing.T) {
	caClient := createTestCAClient(t)

	tests := []struct {
		name           string
		config         *config.PAMOIDCIssuer
		expectError    bool
		expectedIssuer string
	}{
		{
			name: "valid configuration with issuer",
			config: &config.PAMOIDCIssuer{
				Issuer: "https://example.com",
			},
			expectError:    false,
			expectedIssuer: "https://example.com",
		},
		{
			name: "custom configuration with different issuer",
			config: &config.PAMOIDCIssuer{
				Issuer: "https://custom.example.com",
				Scopes: []string{"openid", "profile"},
			},
			expectError:    false,
			expectedIssuer: "https://custom.example.com",
		},
		{
			name:        "missing issuer should error",
			config:      &config.PAMOIDCIssuer{},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewPAMOIDCProvider(caClient, tt.config)
			require.NoError(t, err)

			result, err := provider.GetOpenIDConfiguration()
			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, result)
			assert.NotNil(t, result.Issuer)
			assert.Equal(t, tt.expectedIssuer, *result.Issuer)
		})
	}
}

func TestPAMOIDCProvider_GetJWKS(t *testing.T) {
	// Create a minimal provider for testing
	caClient := createTestCAClient(t)
	cfg := &config.PAMOIDCIssuer{}
	provider, err := NewPAMOIDCProvider(caClient, cfg)
	require.NoError(t, err)

	// Test that GetJWKS calls the JWT generator
	result, err := provider.GetJWKS()

	// The actual result depends on the JWT generator implementation
	// We just verify it doesn't error and returns a proper response
	require.NoError(t, err)
	assert.NotNil(t, result)
	// Verify it has the expected structure for JWKS
	assert.NotNil(t, result.Keys)
}

func TestPAMOIDCProvider_UserInfo(t *testing.T) {
	// Create a minimal provider for testing
	caClient := createTestCAClient(t)
	cfg := &config.PAMOIDCIssuer{}
	provider, err := NewPAMOIDCProvider(caClient, cfg)
	require.NoError(t, err)

	tests := []struct {
		name        string
		accessToken string
		expectError bool
		errorCode   string
	}{
		{
			name:        "invalid token",
			accessToken: "invalid-token",
			expectError: true,
			errorCode:   "invalid_token",
		},
		{
			name:        "empty token",
			accessToken: "",
			expectError: true,
			errorCode:   "invalid_token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := provider.UserInfo(context.Background(), tt.accessToken)

			if tt.expectError {
				require.Error(t, err)
				assert.NotNil(t, response.Error)
				assert.Equal(t, tt.errorCode, *response.Error)
			} else {
				assert.NotNil(t, response)
			}
		})
	}
}

func TestPAMOIDCProvider_InterfaceCompliance(t *testing.T) {
	// Test that PAMOIDCProvider implements the OIDCIssuer interface
	caClient := createTestCAClient(t)
	cfg := &config.PAMOIDCIssuer{
		Issuer: "https://test.com",
	}
	provider, err := NewPAMOIDCProvider(caClient, cfg)
	require.NoError(t, err)

	// This test ensures the provider implements all required interface methods
	var _ OIDCIssuer = provider

	// Test all interface methods exist and can be called
	ctx := context.Background()

	// Token method
	tokenReq := &v1alpha1.TokenRequest{
		GrantType: "unsupported",
	}
	tokenResp, err := provider.Token(ctx, tokenReq)
	require.NoError(t, err)
	assert.NotNil(t, tokenResp)
	assert.NotNil(t, tokenResp.Error)

	// UserInfo method
	userInfoResp, err := provider.UserInfo(ctx, "invalid-token")
	require.Error(t, err)
	assert.NotNil(t, userInfoResp.Error)

	// GetOpenIDConfiguration method
	oidcConfig, err := provider.GetOpenIDConfiguration()
	require.NoError(t, err)
	assert.NotNil(t, oidcConfig)
	assert.NotNil(t, oidcConfig.Issuer)
	assert.Equal(t, "https://test.com", *oidcConfig.Issuer)

	// GetJWKS method
	jwks, err := provider.GetJWKS()
	require.NoError(t, err)
	assert.NotNil(t, jwks)
}

func TestPAMOIDCProvider_AuthorizationCodeFlow(t *testing.T) {
	// Test the authorization code flow with mocked PAM authentication
	mockUser := &user.User{
		Uid:      "1000",
		Gid:      "1000",
		Username: testUsername,
		Name:     "Test User",
		HomeDir:  "/home/testuser",
	}
	mockAuth := NewMockPAMAuthenticator(
		func(username, password string) error {
			return nil
		},
		mockUser,
		[]string{"users", "wheel"},
	)
	caClient := createTestCAClient(t)
	cfg := &config.PAMOIDCIssuer{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURIs: []string{"https://example.com/callback"},
	}
	provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
	require.NoError(t, err)

	t.Run("invalid_authorization_code", func(t *testing.T) {
		// Test with invalid authorization code
		tokenReq := &v1alpha1.TokenRequest{
			GrantType:    v1alpha1.AuthorizationCode,
			Code:         lo.ToPtr("invalid-code"),
			ClientId:     lo.ToPtr("test-client"),
			ClientSecret: lo.ToPtr("test-secret"),
		}

		response, err := provider.Token(context.Background(), tokenReq)
		require.NoError(t, err)
		assert.NotNil(t, response.Error)
		assert.Equal(t, "invalid_grant", *response.Error)
	})

	t.Run("invalid_client_id", func(t *testing.T) {
		// Test with invalid client ID
		tokenReq := &v1alpha1.TokenRequest{
			GrantType: v1alpha1.AuthorizationCode,
			Code:      lo.ToPtr("valid-code"),
			ClientId:  lo.ToPtr("wrong-client"),
		}

		response, err := provider.Token(context.Background(), tokenReq)
		require.NoError(t, err)
		assert.NotNil(t, response.Error)
		assert.Equal(t, "invalid_client", *response.Error)
	})

	t.Run("invalid_client_secret_when_provided", func(t *testing.T) {
		// Test with invalid client secret when provided
		tokenReq := &v1alpha1.TokenRequest{
			GrantType:    v1alpha1.AuthorizationCode,
			Code:         lo.ToPtr("valid-code"),
			ClientId:     lo.ToPtr("test-client"),
			ClientSecret: lo.ToPtr("wrong-secret"),
		}

		response, err := provider.Token(context.Background(), tokenReq)
		require.NoError(t, err)
		assert.NotNil(t, response.Error)
		assert.Equal(t, "invalid_client", *response.Error)
	})

	t.Run("successful_authorization_code_flow", func(t *testing.T) {
		// First, create a valid authorization code by simulating the authorize flow
		authParams := &v1alpha1.AuthAuthorizeParams{
			ClientId:     "test-client",
			RedirectUri:  "https://example.com/callback",
			ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
			State:        lo.ToPtr("test-state"),
		}

		// Create a session for the user
		sessionID := "test-session-123"
		provider.CreateUserSession(sessionID, testUsername, "test-client", "https://example.com/callback", "test-state")

		// Mock the session in context
		ctx := context.WithValue(context.Background(), consts.SessionIDCtxKey, sessionID)

		// Get authorization code
		authResp, err := provider.Authorize(ctx, authParams)
		require.NoError(t, err)
		require.NotNil(t, authResp)
		assert.Equal(t, AuthorizeResponseTypeRedirect, authResp.Type)
		assert.Contains(t, authResp.Content, "code=")

		// Extract the code from the redirect URL
		authCode := authResp.Content
		codeStart := strings.Index(authCode, "code=") + 5
		codeEnd := strings.Index(authCode[codeStart:], "&")
		if codeEnd == -1 {
			codeEnd = len(authCode)
		} else {
			codeEnd = codeStart + codeEnd
		}
		authCodeValue := authCode[codeStart:codeEnd]

		// Now test the token exchange
		tokenReq := &v1alpha1.TokenRequest{
			GrantType:    v1alpha1.AuthorizationCode,
			Code:         lo.ToPtr(authCodeValue),
			ClientId:     lo.ToPtr("test-client"),
			ClientSecret: lo.ToPtr("test-secret"),
		}

		response, err := provider.Token(context.Background(), tokenReq)
		require.NoError(t, err)
		assert.Nil(t, response.Error, "Expected response.Error to be nil, but got: %v", response.Error)

		// Verify successful token response
		require.NotNil(t, response.AccessToken, "Expected response.AccessToken to not be nil")
		require.NotNil(t, response.TokenType, "Expected response.TokenType to not be nil")
		assert.Equal(t, v1alpha1.TokenResponseTokenType("Bearer"), *response.TokenType)
		assert.Equal(t, int(time.Hour.Seconds()), *response.ExpiresIn)

		// Verify the access token contains expected claims
		parsedToken, err := jwt.Parse([]byte(*response.AccessToken), jwt.WithValidate(false), jwt.WithVerify(false))
		require.NoError(t, err)

		// Check that the token contains the test user's information
		sub, exists := parsedToken.Get("sub")
		require.True(t, exists)
		assert.Equal(t, testUsername, sub, "Expected sub claim to be %v, but got %v", testUsername, sub)

		preferredUsername, exists := parsedToken.Get("preferred_username")
		require.True(t, exists)
		assert.Equal(t, testUsername, preferredUsername)

		// Test UserInfo with the generated token
		userInfoResp, err := provider.UserInfo(context.Background(), *response.AccessToken)
		require.NoError(t, err)
		assert.NotNil(t, userInfoResp.Sub)
		assert.Equal(t, testUsername, *userInfoResp.Sub)
	})

	t.Run("authorization_code_with_offline_access", func(t *testing.T) {
		// Test authorization code flow with offline_access scope to get refresh token
		authParams := &v1alpha1.AuthAuthorizeParams{
			ClientId:     "test-client",
			RedirectUri:  "https://example.com/callback",
			ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
			State:        lo.ToPtr("test-state"),
			Scope:        lo.ToPtr("openid profile email offline_access"),
		}

		// Create a session for the user
		sessionID := "test-session-456"
		provider.CreateUserSession(sessionID, testUsername, "test-client", "https://example.com/callback", "test-state")

		// Mock the session in context
		ctx := context.WithValue(context.Background(), consts.SessionIDCtxKey, sessionID)

		// Get authorization code
		authResp, err := provider.Authorize(ctx, authParams)
		require.NoError(t, err)
		require.NotNil(t, authResp)
		assert.Equal(t, AuthorizeResponseTypeRedirect, authResp.Type)
		assert.Contains(t, authResp.Content, "code=")

		// Extract the code from the redirect URL
		authCode := authResp.Content
		codeStart := strings.Index(authCode, "code=") + 5
		codeEnd := strings.Index(authCode[codeStart:], "&")
		if codeEnd == -1 {
			codeEnd = len(authCode)
		} else {
			codeEnd = codeStart + codeEnd
		}
		authCodeValue := authCode[codeStart:codeEnd]

		// Test the token exchange
		tokenReq := &v1alpha1.TokenRequest{
			GrantType:    v1alpha1.AuthorizationCode,
			Code:         lo.ToPtr(authCodeValue),
			ClientId:     lo.ToPtr("test-client"),
			ClientSecret: lo.ToPtr("test-secret"),
		}

		response, err := provider.Token(context.Background(), tokenReq)
		require.NoError(t, err)
		assert.Nil(t, response.Error, "Expected response.Error to be nil, but got: %v", response.Error)

		// Verify both access and refresh tokens are present
		require.NotNil(t, response.AccessToken, "Expected response.AccessToken to not be nil")
		require.NotNil(t, response.RefreshToken, "Expected response.RefreshToken to not be nil")
		require.NotNil(t, response.TokenType, "Expected response.TokenType to not be nil")
		assert.Equal(t, v1alpha1.TokenResponseTokenType("Bearer"), *response.TokenType)
	})

	t.Run("authorization_code_with_client_secret", func(t *testing.T) {
		// Test authorization code flow with client secret provided
		authParams := &v1alpha1.AuthAuthorizeParams{
			ClientId:     "test-client",
			RedirectUri:  "https://example.com/callback",
			ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
			State:        lo.ToPtr("test-state"),
		}

		// Create a session for the user
		sessionID := "test-session-with-secret"
		provider.CreateUserSession(sessionID, testUsername, "test-client", "https://example.com/callback", "test-state")

		// Mock the session in context
		ctx := context.WithValue(context.Background(), consts.SessionIDCtxKey, sessionID)

		// Get authorization code
		authResp, err := provider.Authorize(ctx, authParams)
		require.NoError(t, err)
		require.NotNil(t, authResp)
		assert.Equal(t, AuthorizeResponseTypeRedirect, authResp.Type)
		assert.Contains(t, authResp.Content, "code=")

		// Extract the code from the redirect URL
		authCode := authResp.Content
		codeStart := strings.Index(authCode, "code=") + 5
		codeEnd := strings.Index(authCode[codeStart:], "&")
		if codeEnd == -1 {
			codeEnd = len(authCode)
		} else {
			codeEnd = codeStart + codeEnd
		}
		authCodeValue := authCode[codeStart:codeEnd]

		// Test the token exchange with client secret
		tokenReq := &v1alpha1.TokenRequest{
			GrantType:    v1alpha1.AuthorizationCode,
			Code:         lo.ToPtr(authCodeValue),
			ClientId:     lo.ToPtr("test-client"),
			ClientSecret: lo.ToPtr("test-secret"),
		}

		response, err := provider.Token(context.Background(), tokenReq)
		require.NoError(t, err)
		assert.Nil(t, response.Error, "Expected response.Error to be nil, but got: %v", response.Error)

		// Verify successful token response
		require.NotNil(t, response.AccessToken, "Expected response.AccessToken to not be nil")
		require.NotNil(t, response.TokenType, "Expected response.TokenType to not be nil")
		assert.Equal(t, v1alpha1.TokenResponseTokenType("Bearer"), *response.TokenType)
	})
}

func TestPAMOIDCProvider_RefreshTokenFlow(t *testing.T) {
	// Test the refresh token flow with mocked PAM authentication
	mockUser := &user.User{
		Uid:      "1000",
		Gid:      "1000",
		Username: testUsername,
		Name:     "Test User",
		HomeDir:  "/home/testuser",
	}
	mockAuth := NewMockPAMAuthenticator(
		func(username, password string) error {
			return nil
		},
		mockUser,
		[]string{"users", "wheel"},
	)
	caClient := createTestCAClient(t)
	cfg := &config.PAMOIDCIssuer{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURIs: []string{"https://example.com/callback"},
	}
	provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
	require.NoError(t, err)

	t.Run("invalid_refresh_token", func(t *testing.T) {
		// Test with invalid refresh token
		tokenReq := &v1alpha1.TokenRequest{
			GrantType:    v1alpha1.RefreshToken,
			RefreshToken: lo.ToPtr("invalid-refresh-token"),
		}

		response, err := provider.Token(context.Background(), tokenReq)
		require.NoError(t, err)
		assert.NotNil(t, response.Error)
		assert.Equal(t, "invalid_grant", *response.Error)
	})

	t.Run("successful_refresh_token_flow", func(t *testing.T) {
		// First, create a valid refresh token by going through the authorization code flow
		authParams := &v1alpha1.AuthAuthorizeParams{
			ClientId:     "test-client",
			RedirectUri:  "https://example.com/callback",
			ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
			State:        lo.ToPtr("test-state"),
			Scope:        lo.ToPtr("openid profile email offline_access"),
		}

		// Create a session for the user
		sessionID := "test-session-789"
		provider.CreateUserSession(sessionID, testUsername, "test-client", "https://example.com/callback", "test-state")

		// Mock the session in context
		ctx := context.WithValue(context.Background(), consts.SessionIDCtxKey, sessionID)

		// Get authorization code
		authResp, err := provider.Authorize(ctx, authParams)
		require.NoError(t, err)
		require.NotNil(t, authResp)
		assert.Equal(t, AuthorizeResponseTypeRedirect, authResp.Type)
		assert.Contains(t, authResp.Content, "code=")

		// Extract the code from the redirect URL
		authCode := authResp.Content
		codeStart := strings.Index(authCode, "code=") + 5
		codeEnd := strings.Index(authCode[codeStart:], "&")
		if codeEnd == -1 {
			codeEnd = len(authCode)
		} else {
			codeEnd = codeStart + codeEnd
		}
		authCodeValue := authCode[codeStart:codeEnd]

		// Exchange authorization code for tokens
		tokenReq := &v1alpha1.TokenRequest{
			GrantType:    v1alpha1.AuthorizationCode,
			Code:         lo.ToPtr(authCodeValue),
			ClientId:     lo.ToPtr("test-client"),
			ClientSecret: lo.ToPtr("test-secret"),
		}

		// The provider already has the correct config from the test setup

		response, err := provider.Token(context.Background(), tokenReq)
		require.NoError(t, err)
		assert.Nil(t, response.Error, "Expected response.Error to be nil, but got: %v", response.Error)
		require.NotNil(t, response.RefreshToken, "Expected response.RefreshToken to not be nil")

		// Now test the refresh token flow
		refreshReq := &v1alpha1.TokenRequest{
			GrantType:    v1alpha1.RefreshToken,
			RefreshToken: response.RefreshToken,
		}

		refreshResponse, err := provider.Token(context.Background(), refreshReq)
		require.NoError(t, err)
		assert.Nil(t, refreshResponse.Error)

		// Verify successful refresh token response
		require.NotNil(t, refreshResponse.AccessToken)
		require.NotNil(t, refreshResponse.RefreshToken) // New refresh token should be issued
		require.NotNil(t, refreshResponse.TokenType)
		assert.Equal(t, v1alpha1.TokenResponseTokenType("Bearer"), *refreshResponse.TokenType)
		assert.Equal(t, int(time.Hour.Seconds()), *refreshResponse.ExpiresIn)

		// Verify the new access token contains expected claims
		parsedToken, err := jwt.Parse([]byte(*refreshResponse.AccessToken), jwt.WithValidate(false), jwt.WithVerify(false))
		require.NoError(t, err)

		// Check that the token contains the test user's information
		sub, exists := parsedToken.Get("sub")
		require.True(t, exists)
		assert.Equal(t, testUsername, sub, "Expected sub claim to be %v, but got %v", testUsername, sub)

		preferredUsername, exists := parsedToken.Get("preferred_username")
		require.True(t, exists)
		assert.Equal(t, testUsername, preferredUsername)

		// Test UserInfo with the refreshed token
		userInfoResp, err := provider.UserInfo(context.Background(), *refreshResponse.AccessToken)
		require.NoError(t, err)
		assert.NotNil(t, userInfoResp.Sub)
		assert.Equal(t, testUsername, *userInfoResp.Sub)
	})

	t.Run("refresh_token_with_updated_user_info", func(t *testing.T) {
		// Test that refresh token flow updates user information from NSS
		// This simulates a scenario where user groups have changed since the original token was issued

		// Create a new mock authenticator with updated user groups
		updatedMockAuth := NewMockPAMAuthenticator(
			func(username, password string) error {
				return nil
			},
			mockUser,
			[]string{"users", "wheel", "flightctl-admin"}, // Updated groups
		)

		// Create a new provider with the updated authenticator
		caClient := createTestCAClient(t)
		cfg := &config.PAMOIDCIssuer{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
		}
		updatedProvider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, updatedMockAuth)
		require.NoError(t, err)

		// Create a valid refresh token (simplified for this test)
		identity := common.NewBaseIdentity(testUsername, testUsername, []common.ReportedOrganization{{Name: "default", IsInternalID: false, ID: "default"}}, []string{"viewer"})
		tokenGenerationRequest := authn.TokenGenerationRequest{
			Username:      identity.GetUsername(),
			UID:           identity.GetUID(),
			Organizations: []string{"default"},
			Roles:         identity.GetRoles(),
		}
		refreshToken, err := updatedProvider.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, 7*24*time.Hour, "refresh_token")
		require.NoError(t, err)

		// Test refresh token flow
		refreshReq := &v1alpha1.TokenRequest{
			GrantType:    v1alpha1.RefreshToken,
			RefreshToken: lo.ToPtr(refreshToken),
		}

		refreshResponse, err := updatedProvider.Token(context.Background(), refreshReq)
		require.NoError(t, err)
		assert.Nil(t, refreshResponse.Error)

		// Verify the new access token contains updated user information
		parsedToken, err := jwt.Parse([]byte(*refreshResponse.AccessToken), jwt.WithValidate(false), jwt.WithVerify(false))
		require.NoError(t, err)

		// Check that the token contains the test user's information
		sub, exists := parsedToken.Get("sub")
		require.True(t, exists)
		assert.Equal(t, testUsername, sub, "Expected sub claim to be %v, but got %v", testUsername, sub)

		// Verify roles are updated based on current group membership
		rolesInterface, exists := parsedToken.Get("roles")
		require.True(t, exists)

		var roles []string
		switch v := rolesInterface.(type) {
		case []string:
			roles = v
		case []interface{}:
			roles = make([]string, len(v))
			for i, role := range v {
				if roleStr, ok := role.(string); ok {
					roles[i] = roleStr
				}
			}
		}

		// Should include admin role from flightctl-admin group
		assert.Contains(t, roles, "admin")
	})
}

func TestPAMOIDCProvider_UserInfoClaims(t *testing.T) {
	// Test that UserInfo returns proper claims structure using authorization code flow
	mockUser := &user.User{
		Uid:      "1000",
		Gid:      "1000",
		Username: testUsername,
		Name:     "Test User",
		HomeDir:  "/home/testuser",
	}
	mockAuth := NewMockPAMAuthenticator(
		func(username, password string) error {
			return nil
		},
		mockUser,
		[]string{"users", "wheel"},
	)
	caClient := createTestCAClient(t)
	cfg := &config.PAMOIDCIssuer{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURIs: []string{"https://example.com/callback"},
	}
	provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
	require.NoError(t, err)

	// Create a session and get authorization code
	authParams := &v1alpha1.AuthAuthorizeParams{
		ClientId:     "test-client",
		RedirectUri:  "https://example.com/callback",
		ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
		State:        lo.ToPtr("test-state"),
	}

	sessionID := "test-session-userinfo"
	provider.CreateUserSession(sessionID, testUsername, "test-client", "https://example.com/callback", "test-state")
	ctx := context.WithValue(context.Background(), consts.SessionIDCtxKey, sessionID)

	authResp, err := provider.Authorize(ctx, authParams)
	require.NoError(t, err)
	require.NotNil(t, authResp)
	assert.Equal(t, AuthorizeResponseTypeRedirect, authResp.Type)
	assert.Contains(t, authResp.Content, "code=")

	// Extract the code from the redirect URL
	authCode := authResp.Content
	codeStart := strings.Index(authCode, "code=") + 5
	codeEnd := strings.Index(authCode[codeStart:], "&")
	if codeEnd == -1 {
		codeEnd = len(authCode)
	} else {
		codeEnd = codeStart + codeEnd
	}
	authCodeValue := authCode[codeStart:codeEnd]

	// Exchange authorization code for tokens
	tokenReq := &v1alpha1.TokenRequest{
		GrantType:    v1alpha1.AuthorizationCode,
		Code:         lo.ToPtr(authCodeValue),
		ClientId:     lo.ToPtr("test-client"),
		ClientSecret: lo.ToPtr("test-secret"),
	}

	response, err := provider.Token(context.Background(), tokenReq)
	require.NoError(t, err)
	assert.Nil(t, response.Error)
	require.NotNil(t, response.AccessToken)

	// Test UserInfo with the token generated by PAM issuer
	userInfoResp, err := provider.UserInfo(context.Background(), *response.AccessToken)
	require.NoError(t, err)
	assert.NotNil(t, userInfoResp)

	// Verify the UserInfo response structure
	assert.NotNil(t, userInfoResp.Sub)
	assert.Equal(t, testUsername, *userInfoResp.Sub)

	assert.NotNil(t, userInfoResp.PreferredUsername)
	assert.Equal(t, testUsername, *userInfoResp.PreferredUsername)

	assert.NotNil(t, userInfoResp.Name)
	// The name should be set by the PAM issuer from system user lookup
	assert.Equal(t, "Test User", *userInfoResp.Name)

	assert.NotNil(t, userInfoResp.Email)
	// Email might be empty if not available from system user
	assert.Equal(t, "", *userInfoResp.Email)

	assert.NotNil(t, userInfoResp.EmailVerified)
	assert.False(t, *userInfoResp.EmailVerified) // Default to false

	assert.NotNil(t, userInfoResp.Roles)
	roles := *userInfoResp.Roles
	// The roles should come from the system user's groups
	// "wheel" group should map to "admin" role
	assert.Contains(t, roles, "admin")

	assert.NotNil(t, userInfoResp.Organizations)
	// Organizations should default to ["default"] when no org: groups are present
	orgs := *userInfoResp.Organizations
	assert.Contains(t, orgs, "default")
}

func TestPAMOIDCProvider_EndToEndFlow(t *testing.T) {
	// Test the complete flow: Authorization Code -> Token -> UserInfo -> Claims verification
	mockUser := &user.User{
		Uid:      "1000",
		Gid:      "1000",
		Username: testUsername,
		Name:     "Test User",
		HomeDir:  "/home/testuser",
	}
	mockAuth := NewMockPAMAuthenticator(
		func(username, password string) error {
			return nil
		},
		mockUser,
		[]string{"users", "wheel"},
	)
	caClient := createTestCAClient(t)
	cfg := &config.PAMOIDCIssuer{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURIs: []string{"https://example.com/callback"},
	}
	provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
	require.NoError(t, err)

	// Step 1: Authorization Code Flow
	authParams := &v1alpha1.AuthAuthorizeParams{
		ClientId:     "test-client",
		RedirectUri:  "https://example.com/callback",
		ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
		State:        lo.ToPtr("test-state"),
		Scope:        lo.ToPtr("openid profile email offline_access"),
	}

	sessionID := "test-session-e2e"
	provider.CreateUserSession(sessionID, testUsername, "test-client", "https://example.com/callback", "test-state")
	ctx := context.WithValue(context.Background(), consts.SessionIDCtxKey, sessionID)

	authResp, err := provider.Authorize(ctx, authParams)
	require.NoError(t, err)
	require.NotNil(t, authResp)
	assert.Equal(t, AuthorizeResponseTypeRedirect, authResp.Type)
	assert.Contains(t, authResp.Content, "code=")

	// Extract the code from the redirect URL
	authCode := authResp.Content
	codeStart := strings.Index(authCode, "code=") + 5
	codeEnd := strings.Index(authCode[codeStart:], "&")
	if codeEnd == -1 {
		codeEnd = len(authCode)
	} else {
		codeEnd = codeStart + codeEnd
	}
	authCodeValue := authCode[codeStart:codeEnd]

	// Step 2: Exchange authorization code for tokens
	tokenReq := &v1alpha1.TokenRequest{
		GrantType:    v1alpha1.AuthorizationCode,
		Code:         lo.ToPtr(authCodeValue),
		ClientId:     lo.ToPtr("test-client"),
		ClientSecret: lo.ToPtr("test-secret"),
	}

	response, err := provider.Token(context.Background(), tokenReq)
	require.NoError(t, err)
	assert.Nil(t, response.Error)

	// Verify successful token response
	require.NotNil(t, response.AccessToken)
	require.NotNil(t, response.RefreshToken)
	require.NotNil(t, response.TokenType)
	assert.Equal(t, v1alpha1.TokenResponseTokenType("Bearer"), *response.TokenType)

	// Test 1: Verify access token claims
	parsedToken, err := jwt.Parse([]byte(*response.AccessToken), jwt.WithValidate(false), jwt.WithVerify(false))
	require.NoError(t, err)

	// Verify all expected claims are present
	claims := map[string]interface{}{}
	_ = parsedToken.Walk(context.Background(), jwt.VisitorFunc(func(key string, value interface{}) error {
		claims[key] = value
		return nil
	}))

	// Verify standard claims
	assert.Equal(t, testUsername, claims["sub"])
	assert.Equal(t, testUsername, claims["preferred_username"])
	assert.Equal(t, "access_token", claims["token_type"])
	assert.NotNil(t, claims["exp"])
	assert.NotNil(t, claims["iat"])
	assert.NotNil(t, claims["nbf"])

	// Verify custom claims
	rolesInterface, exists := claims["roles"]
	require.True(t, exists, "roles claim should exist")

	// Handle different possible types for roles
	var roles []string
	switch v := rolesInterface.(type) {
	case []string:
		roles = v
	case []interface{}:
		roles = make([]string, len(v))
		for i, role := range v {
			if roleStr, ok := role.(string); ok {
				roles[i] = roleStr
			}
		}
	default:
		t.Fatalf("Unexpected roles type: %T", v)
	}

	// Verify the specific roles we mocked (wheel maps to admin)
	assert.Contains(t, roles, "admin")

	// Test 2: Verify UserInfo response
	userInfoResp, err := provider.UserInfo(context.Background(), *response.AccessToken)
	require.NoError(t, err)
	assert.NotNil(t, userInfoResp.Sub)
	assert.Equal(t, testUsername, *userInfoResp.Sub)

	// Test 3: Verify refresh token structure
	parsedRefreshToken, err := jwt.Parse([]byte(*response.RefreshToken), jwt.WithValidate(false), jwt.WithVerify(false))
	require.NoError(t, err)

	refreshClaims := map[string]interface{}{}
	_ = parsedRefreshToken.Walk(context.Background(), jwt.VisitorFunc(func(key string, value interface{}) error {
		refreshClaims[key] = value
		return nil
	}))

	assert.Equal(t, testUsername, refreshClaims["sub"])
	assert.Equal(t, "refresh_token", refreshClaims["token_type"])

	// Test 4: Verify refresh token flow
	refreshReq := &v1alpha1.TokenRequest{
		GrantType:    v1alpha1.RefreshToken,
		RefreshToken: response.RefreshToken,
	}

	refreshResponse, err := provider.Token(context.Background(), refreshReq)
	require.NoError(t, err)
	assert.Nil(t, refreshResponse.Error)

	// Verify new tokens are issued
	require.NotNil(t, refreshResponse.AccessToken)
	require.NotNil(t, refreshResponse.RefreshToken)
	assert.NotEqual(t, *response.AccessToken, *refreshResponse.AccessToken)
	assert.NotEqual(t, *response.RefreshToken, *refreshResponse.RefreshToken)

	// Test 5: Verify UserInfo with refreshed token
	refreshedUserInfoResp, err := provider.UserInfo(context.Background(), *refreshResponse.AccessToken)
	require.NoError(t, err)
	assert.NotNil(t, refreshedUserInfoResp.Sub)
	assert.Equal(t, testUsername, *refreshedUserInfoResp.Sub)
}

func TestPAMOIDCProvider_TokenValidation(t *testing.T) {
	// Test that generated tokens can be validated by the JWT generator
	caClient := createTestCAClient(t)
	cfg := &config.PAMOIDCIssuer{}
	provider, err := NewPAMOIDCProvider(caClient, cfg)
	require.NoError(t, err)

	// Create a test identity
	identity := common.NewBaseIdentity("testuser", "testuser", []common.ReportedOrganization{}, []string{"admin"})
	tokenGenerationRequest := authn.TokenGenerationRequest{
		Username:      identity.GetUsername(),
		UID:           identity.GetUID(),
		Organizations: []string{},
		Roles:         identity.GetRoles(),
	}

	// Generate access token
	accessToken, err := provider.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, time.Hour, "access_token")
	require.NoError(t, err)

	// Generate refresh token
	refreshToken, err := provider.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, 7*24*time.Hour, "refresh_token")
	require.NoError(t, err)

	// Validate access token
	validatedIdentity, err := provider.jwtGenerator.ValidateTokenWithType(accessToken, "access_token")
	require.NoError(t, err)
	assert.Equal(t, "testuser", validatedIdentity.GetUsername())
	assert.Equal(t, "testuser", validatedIdentity.GetUID())

	// Validate refresh token
	validatedRefreshIdentity, err := provider.jwtGenerator.ValidateTokenWithType(refreshToken, "refresh_token")
	require.NoError(t, err)
	assert.Equal(t, "testuser", validatedRefreshIdentity.GetUsername())
	assert.Equal(t, "testuser", validatedRefreshIdentity.GetUID())

	// Test that wrong token type validation fails
	_, err = provider.jwtGenerator.ValidateTokenWithType(accessToken, "refresh_token")
	require.Error(t, err)

	_, err = provider.jwtGenerator.ValidateTokenWithType(refreshToken, "access_token")
	require.Error(t, err)

	// Test that invalid tokens fail validation
	_, err = provider.jwtGenerator.ValidateTokenWithType("invalid-token", "access_token")
	require.Error(t, err)
}
