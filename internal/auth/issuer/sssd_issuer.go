//go:build linux

package issuer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// Default group to role mapping
var defaultGroupRoleMap = map[string]string{
	"flightctl-admin":     v1alpha1.RoleAdmin,
	"flightctl-operator":  v1alpha1.RoleOperator,
	"flightctl-viewer":    v1alpha1.RoleViewer,
	"flightctl-installer": v1alpha1.RoleInstaller,
	"wheel":               v1alpha1.RoleAdmin,    // Traditional Unix admin group
	"sudo":                v1alpha1.RoleAdmin,    // Sudo users get admin access
	"adm":                 v1alpha1.RoleOperator, // System administration group
}

// AuthorizationCodeData represents stored authorization code data
type AuthorizationCodeData struct {
	Code        string
	ClientID    string
	RedirectURI string
	Scope       string
	State       string
	Username    string
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

// AuthorizationCodeStore manages temporary authorization codes
type AuthorizationCodeStore struct {
	codes sync.Map
}

// NewAuthorizationCodeStore creates a new authorization code store
func NewAuthorizationCodeStore() *AuthorizationCodeStore {
	return &AuthorizationCodeStore{}
}

// StoreCode stores an authorization code with expiration
func (s *AuthorizationCodeStore) StoreCode(codeData *AuthorizationCodeData) {
	s.codes.Store(codeData.Code, codeData)
}

// GetCode retrieves and removes an authorization code
func (s *AuthorizationCodeStore) GetCode(code string) (*AuthorizationCodeData, bool) {
	value, exists := s.codes.Load(code)
	if !exists {
		return nil, false
	}

	codeData, ok := value.(*AuthorizationCodeData)
	if !ok {
		s.codes.Delete(code)
		return nil, false
	}

	// Check if code has expired
	if time.Now().After(codeData.ExpiresAt) {
		s.codes.Delete(code)
		return nil, false
	}

	// Remove the code (single use)
	s.codes.Delete(code)
	return codeData, true
}

// CleanupExpiredCodes removes expired codes
func (s *AuthorizationCodeStore) CleanupExpiredCodes() {
	now := time.Now()
	s.codes.Range(func(key, value interface{}) bool {
		code := key.(string)
		codeData := value.(*AuthorizationCodeData)
		if now.After(codeData.ExpiresAt) {
			s.codes.Delete(code)
		}
		return true // Continue iteration
	})
}

// generateAuthorizationCode generates a secure random authorization code
func generateAuthorizationCode() (string, error) {
	bytes := make([]byte, 32) // 256 bits of entropy
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// SSSDOIDCProvider handles OIDC-compliant authentication flows using SSSD

// NewSSSDOIDCProvider creates a new SSSD-based OIDC provider
func NewSSSDOIDCProvider(caClient *fccrypto.CAClient, config *config.SSSDOIDCIssuer) (*SSSDOIDCProvider, error) {
	return NewSSSDOIDCProviderWithAuthenticator(caClient, config, nil)
}

// NewSSSDOIDCProviderWithAuthenticator creates a new SSSD-based OIDC provider with a custom authenticator
func NewSSSDOIDCProviderWithAuthenticator(caClient *fccrypto.CAClient, config *config.SSSDOIDCIssuer, sssdAuth SSSDAuthenticator) (*SSSDOIDCProvider, error) {
	jwtGen, err := authn.NewJWTGenerator(caClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT generator: %w", err)
	}

	// Create default authenticator if none provided
	if sssdAuth == nil {
		sssdAuth, err = NewRealSSSDAuthenticator()
		if err != nil {
			return nil, fmt.Errorf("failed to create SSSD authenticator: %w", err)
		}
	}

	return &SSSDOIDCProvider{
		jwtGenerator:      jwtGen,
		config:            config,
		sssdAuthenticator: sssdAuth,
		codeStore:         NewAuthorizationCodeStore(),
		sessionStore:      NewSessionStore(),
		log:               logrus.New(),
	}, nil
}

// Token implements OIDCProvider interface - handles OAuth2 token requests
func (s *SSSDOIDCProvider) Token(ctx context.Context, req *v1alpha1.TokenRequest) (*v1alpha1.TokenResponse, error) {
	// Handle different grant types - only OIDC-compliant flows
	switch req.GrantType {
	case v1alpha1.RefreshToken:
		return s.handleRefreshTokenGrant(ctx, req)
	case v1alpha1.AuthorizationCode:
		return s.handleAuthorizationCodeGrant(ctx, req)
	default:
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("unsupported_grant_type")}, nil
	}
}

// handleRefreshTokenGrant handles the refresh_token grant type
func (s *SSSDOIDCProvider) handleRefreshTokenGrant(ctx context.Context, req *v1alpha1.TokenRequest) (*v1alpha1.TokenResponse, error) {
	// Validate required fields for refresh token flow
	if req.RefreshToken == nil || *req.RefreshToken == "" {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("invalid_request")}, nil
	}

	// Validate the refresh token and ensure it's actually a refresh token
	identity, err := s.jwtGenerator.ValidateTokenWithType(*req.RefreshToken, "refresh_token")
	if err != nil {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("invalid_grant")}, nil
	}

	// Get current user information from SSSD to ensure user still exists
	systemUser, err := s.sssdAuthenticator.LookupUser(identity.GetUsername())
	if err != nil {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("invalid_grant")}, nil
	}

	// Get current user groups for roles
	groups, err := s.sssdAuthenticator.GetUserGroups(systemUser)
	if err != nil {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("server_error")}, nil
	}

	// Map groups to roles and extract organizations
	roles := s.mapGroupsToRoles(groups)
	organizations := s.extractOrganizations(groups)
	tokenGenerationRequest := authn.TokenGenerationRequest{
		Username:      identity.GetUsername(),
		UID:           identity.GetUID(),
		Organizations: organizations,
		Roles:         roles,
	}
	// Generate new access token with proper expiry (1 hour)
	accessToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, time.Hour, "access_token")
	if err != nil {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("server_error")}, nil
	}

	// Create token response
	tokenResponse := &v1alpha1.TokenResponse{
		AccessToken: lo.ToPtr(accessToken),
		TokenType:   lo.ToPtr(v1alpha1.Bearer),
		ExpiresIn:   lo.ToPtr(int(time.Hour.Seconds())),
	}

	// Always issue a new refresh token when using refresh_token grant
	// (if we have a refresh token, it means offline_access was originally granted)
	refreshToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, 7*24*time.Hour, "refresh_token")
	if err != nil {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("server_error")}, nil
	}
	tokenResponse.RefreshToken = lo.ToPtr(refreshToken)

	return tokenResponse, nil
}

// handleAuthorizationCodeGrant handles the authorization_code grant type
func (s *SSSDOIDCProvider) handleAuthorizationCodeGrant(ctx context.Context, req *v1alpha1.TokenRequest) (*v1alpha1.TokenResponse, error) {
	// Validate required fields for authorization code flow
	if req.Code == nil || *req.Code == "" {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("invalid_request")}, nil
	}

	// Validate client credentials
	if req.ClientId == nil || req.ClientSecret == nil {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("invalid_client")}, nil
	}

	// Validate client credentials against config
	if s.config == nil || s.config.ClientID != *req.ClientId || s.config.ClientSecret != *req.ClientSecret {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("invalid_client")}, nil
	}

	// Validate and retrieve authorization code
	codeData, exists := s.codeStore.GetCode(*req.Code)
	if !exists {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("invalid_grant")}, nil
	}

	// Validate that the client ID matches the stored code
	if codeData.ClientID != *req.ClientId {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("invalid_grant")}, nil
	}

	// Get user information from SSSD
	systemUser, err := s.sssdAuthenticator.LookupUser(codeData.Username)
	if err != nil {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("invalid_grant")}, nil
	}

	// Get user groups for roles
	groups, err := s.sssdAuthenticator.GetUserGroups(systemUser)
	if err != nil {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("server_error")}, nil
	}

	// Map groups to roles and extract organizations
	roles := s.mapGroupsToRoles(groups)
	organizations := s.extractOrganizations(groups)

	// Create identity for token generation
	tokenGenerationRequest := authn.TokenGenerationRequest{
		Username:      codeData.Username,
		UID:           codeData.Username,
		Organizations: organizations,
		Roles:         roles,
	}
	// Generate access token with proper expiry (1 hour)
	accessToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, time.Hour, "access_token")
	if err != nil {
		return &v1alpha1.TokenResponse{Error: lo.ToPtr("server_error")}, nil
	}

	// Create token response
	tokenResponse := &v1alpha1.TokenResponse{
		AccessToken: lo.ToPtr(accessToken),
		TokenType:   lo.ToPtr(v1alpha1.Bearer),
		ExpiresIn:   lo.ToPtr(int(time.Hour.Seconds())),
	}

	// Only generate refresh token if offline_access was requested
	if strings.Contains(codeData.Scope, "offline_access") {
		refreshToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, 7*24*time.Hour, "refresh_token")
		if err != nil {
			return &v1alpha1.TokenResponse{Error: lo.ToPtr("server_error")}, nil
		}
		tokenResponse.RefreshToken = lo.ToPtr(refreshToken)
	}

	return tokenResponse, nil
}

// Authorize handles the authorization endpoint for authorization code flow
func (s *SSSDOIDCProvider) Authorize(ctx context.Context, req *v1alpha1.AuthAuthorizeParams) (string, error) {
	s.log.Infof("Authorize: starting authorization request - ClientId=%s, RedirectUri=%s, ResponseType=%s, Scope=%s, State=%s",
		req.ClientId, req.RedirectUri, req.ResponseType, lo.FromPtrOr(req.Scope, ""), lo.FromPtrOr(req.State, ""))

	// Validate required fields
	if req.ClientId == "" || req.RedirectUri == "" {
		s.log.Warnf("Authorize: missing required fields - ClientId=%s, RedirectUri=%s", req.ClientId, req.RedirectUri)
		return "", fmt.Errorf("invalid_request")
	}

	// Validate client ID
	if s.config == nil {
		s.log.Warnf("Authorize: config is nil")
		return "", fmt.Errorf("invalid_client")
	}
	if s.config.ClientID != req.ClientId {
		s.log.Warnf("Authorize: invalid client ID - expected=%s, got=%s", s.config.ClientID, req.ClientId)
		return "", fmt.Errorf("invalid_client")
	}
	s.log.Infof("Authorize: client ID validation passed - %s", req.ClientId)

	// Validate redirect URI
	validRedirect := false
	if s.config != nil {
		s.log.Infof("Authorize: checking redirect URI - requested=%s, allowed=%v", req.RedirectUri, s.config.RedirectURIs)
		for _, uri := range s.config.RedirectURIs {
			if uri == req.RedirectUri {
				validRedirect = true
				s.log.Infof("Authorize: redirect URI validation passed - %s", req.RedirectUri)
				break
			}
		}
	}
	if !validRedirect {
		s.log.Warnf("Authorize: invalid redirect URI - %s", req.RedirectUri)
		return "", fmt.Errorf("invalid_request")
	}

	// Validate response type
	if req.ResponseType != v1alpha1.AuthAuthorizeParamsResponseTypeCode {
		s.log.Warnf("Authorize: unsupported response type - %s", req.ResponseType)
		return "", fmt.Errorf("unsupported_response_type")
	}
	s.log.Infof("Authorize: response type validation passed - %s", req.ResponseType)

	// Authorization flow:
	// 1. Check if user is already authenticated (session/cookie)
	// 2. If not authenticated, return embedded login form
	// 3. User submits credentials via POST to /auth/login
	// 4. Server validates with SSSD and generates authorization code
	// 5. Server redirects back to client with code

	// Extract session ID from request context
	sessionID := s.extractSessionID(ctx, req)
	s.log.Infof("Authorize: extracted session ID - %s", sessionID)

	// Check if user is already authenticated via session
	if sessionID == "" {
		s.log.Infof("Authorize: no session ID found, returning login form")
		// User not authenticated, return embedded login form
		loginForm := s.GetLoginForm(req.ClientId, req.RedirectUri, lo.FromPtrOr(req.State, ""))
		return loginForm, nil
	}

	// Check if session exists and is valid
	sessionData, exists := s.IsUserAuthenticated(sessionID)
	if !exists {
		s.log.Infof("Authorize: session not found or expired - %s, returning login form", sessionID)
		// Session invalid or expired, return login form
		loginForm := s.GetLoginForm(req.ClientId, req.RedirectUri, lo.FromPtrOr(req.State, ""))
		return loginForm, nil
	}

	// User is authenticated, get username from session
	username := sessionData.Username
	s.log.Infof("Authorize: user authenticated via session - username=%s, sessionID=%s", username, sessionID)

	// Generate authorization code
	authCode, err := generateAuthorizationCode()
	if err != nil {
		s.log.Errorf("Authorize: failed to generate authorization code - %v", err)
		return "", fmt.Errorf("server_error")
	}
	s.log.Infof("Authorize: generated authorization code - %s", authCode)

	// Use the requested scope if provided, otherwise determine based on user's role/group membership
	scopes := lo.FromPtrOr(req.Scope, s.determineUserScopes(username))
	s.log.Infof("Authorize: determined scopes - %s", scopes)

	// Store authorization code with expiration (10 minutes)
	codeData := &AuthorizationCodeData{
		Code:        authCode,
		ClientID:    req.ClientId,
		RedirectURI: req.RedirectUri,
		Scope:       scopes,
		State:       lo.FromPtrOr(req.State, ""),
		Username:    username,
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		CreatedAt:   time.Now(),
	}

	s.codeStore.StoreCode(codeData)
	s.log.Infof("Authorize: stored authorization code for user - %s", username)

	// Build redirect URL with authorization code
	redirectURL := fmt.Sprintf("%s?code=%s", req.RedirectUri, authCode)
	if req.State != nil && *req.State != "" {
		redirectURL += fmt.Sprintf("&state=%s", *req.State)
	}

	s.log.Infof("Authorize: returning redirect URL - %s", redirectURL)
	return redirectURL, nil
}

// Login handles the login form submission
func (s *SSSDOIDCProvider) Login(ctx context.Context, username, password, clientID, redirectURI, state string) (string, error) {
	// Validate credentials with SSSD
	if err := s.authenticateWithSSSD(username, password); err != nil {
		return "", fmt.Errorf("invalid_credentials")
	}

	// User is authenticated, create session
	sessionID, err := generateAuthorizationCode() // Reuse the same secure random generation
	if err != nil {
		return "", fmt.Errorf("server_error")
	}

	// Create user session
	s.CreateUserSession(sessionID, username, clientID, redirectURI, state)

	// Redirect back to authorization endpoint with session ID
	authURL := fmt.Sprintf("/api/v1/auth/authorize?response_type=code&client_id=%s&redirect_uri=%s", clientID, redirectURI)
	if state != "" {
		authURL += fmt.Sprintf("&state=%s", state)
	}
	authURL += fmt.Sprintf("&session_id=%s", sessionID)

	return authURL, nil
}

// SessionData represents user session information
type SessionData struct {
	Username    string
	LoginTime   time.Time
	ExpiresAt   time.Time
	ClientID    string
	RedirectURI string
	State       string
}

// SessionStore manages user sessions
type SessionStore struct {
	sessions sync.Map
}

// NewSessionStore creates a new session store
func NewSessionStore() *SessionStore {
	return &SessionStore{}
}

// CreateSession creates a new user session
func (s *SessionStore) CreateSession(sessionID string, data *SessionData) {
	s.sessions.Store(sessionID, data)
}

// GetSession retrieves a session by ID
func (s *SessionStore) GetSession(sessionID string) (*SessionData, bool) {
	value, exists := s.sessions.Load(sessionID)
	if !exists {
		return nil, false
	}

	sessionData, ok := value.(*SessionData)
	if !ok {
		s.sessions.Delete(sessionID)
		return nil, false
	}

	// Check if session has expired
	if time.Now().After(sessionData.ExpiresAt) {
		s.sessions.Delete(sessionID)
		return nil, false
	}

	return sessionData, true
}

// DeleteSession removes a session
func (s *SessionStore) DeleteSession(sessionID string) {
	s.sessions.Delete(sessionID)
}

// CleanupExpiredSessions removes expired sessions
func (s *SessionStore) CleanupExpiredSessions() {
	now := time.Now()
	s.sessions.Range(func(key, value interface{}) bool {
		sessionID := key.(string)
		sessionData := value.(*SessionData)
		if now.After(sessionData.ExpiresAt) {
			s.sessions.Delete(sessionID)
		}
		return true
	})
}

// authenticateWithSSSD authenticates a user using SSSD
func (s *SSSDOIDCProvider) authenticateWithSSSD(username, password string) error {
	return s.sssdAuthenticator.Authenticate(username, password)
}

// determineUserScopes determines the scopes to grant - just the standard OIDC scopes
func (s *SSSDOIDCProvider) determineUserScopes(username string) string {
	// Standard OIDC scopes for all authenticated users
	return "openid profile email"
}

// GetLoginFormWithError generates HTML for the embedded login form with error message
func (s *SSSDOIDCProvider) GetLoginFormWithError(clientID, redirectURI, state, errorMessage string) string {
	return fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
    <title>Flightctl Login</title>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            margin: 0;
            padding: 0;
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
        }
        
        .login-container {
            background: white;
            padding: 2rem;
            border-radius: 12px;
            box-shadow: 0 20px 40px rgba(0, 0, 0, 0.1);
            width: 100%%;
            max-width: 400px;
            margin: 1rem;
        }
        
        .header {
            text-align: center;
            margin-bottom: 2rem;
        }
        
        .header h1 {
            color: #333;
            margin: 0 0 0.5rem 0;
            font-size: 1.8rem;
            font-weight: 600;
        }
        
        .header p {
            color: #666;
            margin: 0;
            font-size: 0.95rem;
        }
        
        .form-group {
            margin-bottom: 1.5rem;
        }
        
        .form-group label {
            display: block;
            margin-bottom: 0.5rem;
            color: #333;
            font-weight: 500;
            font-size: 0.9rem;
        }
        
        .form-group input {
            width: 100%%;
            padding: 0.75rem;
            border: 2px solid #e1e5e9;
            border-radius: 8px;
            font-size: 1rem;
            transition: border-color 0.2s ease;
            box-sizing: border-box;
        }
        
        .form-group input:focus {
            outline: none;
            border-color: #667eea;
            box-shadow: 0 0 0 3px rgba(102, 126, 234, 0.1);
        }
        
        button {
            width: 100%%;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            color: white;
            border: none;
            padding: 0.875rem;
            border-radius: 8px;
            font-size: 1rem;
            font-weight: 600;
            cursor: pointer;
            transition: transform 0.2s ease, box-shadow 0.2s ease;
        }
        
        button:hover {
            transform: translateY(-1px);
            box-shadow: 0 8px 25px rgba(102, 126, 234, 0.3);
        }
        
        button:active {
            transform: translateY(0);
        }
        
        button:disabled {
            opacity: 0.6;
            cursor: not-allowed;
            transform: none;
        }
        
        .error {
            color: #e74c3c;
            font-size: 0.875rem;
            margin-top: 1rem;
            text-align: center;
            display: block;
            background: #fdf2f2;
            border: 1px solid #fecaca;
            border-radius: 6px;
            padding: 0.75rem;
        }
        
        @media (max-width: 480px) {
            .login-container {
                margin: 0.5rem;
                padding: 1.5rem;
            }
        }
    </style>
    <script>
        function handleSubmit(event) {
            event.preventDefault();
            const form = event.target;
            const formData = new FormData(form);
            
            // Show loading state
            const button = form.querySelector('button');
            const originalText = button.textContent;
            button.textContent = 'Logging in...';
            button.disabled = true;
            
            // Submit form data
            fetch('/auth/login', {
                method: 'POST',
                body: formData
            })
            .then(response => {
                if (response.ok) {
                    // Parse the redirect URL from response
                    return response.text();
                } else {
                    throw new Error('Login failed');
                }
            })
            .then(data => {
                // If we get a redirect URL, follow it
                if (data.startsWith('http') || data.startsWith('/')) {
                    window.location.href = data;
                } else {
                    // Otherwise, it might be an error message
                    showError(data);
                }
            })
            .catch(error => {
                showError('Login failed. Please check your credentials.');
            })
            .finally(() => {
                button.textContent = originalText;
                button.disabled = false;
            });
        }
        
        function showError(message) {
            const errorDiv = document.querySelector('.error');
            errorDiv.textContent = message;
            errorDiv.style.display = 'block';
        }
    </script>
</head>
<body>
    <div class="login-container">
        <div class="header">
            <h1>Flightctl</h1>
            <p>Please sign in to continue</p>
        </div>
        
        <form onsubmit="handleSubmit(event)">
            <input type="hidden" name="response_type" value="code">
            <input type="hidden" name="client_id" value="%s">
            <input type="hidden" name="redirect_uri" value="%s">
            <input type="hidden" name="state" value="%s">
            
            <div class="form-group">
                <label for="username">Username:</label>
                <input type="text" id="username" name="scope" required autocomplete="username">
            </div>
            
            <div class="form-group">
                <label for="password">Password:</label>
                <input type="password" id="password" name="password" required autocomplete="current-password">
            </div>
            
            <button type="submit">Sign In</button>
            
            <div class="error">%s</div>
        </form>
    </div>
</body>
</html>`, clientID, redirectURI, state, errorMessage)
}

// UserInfo implements OIDCProvider interface - returns user information
func (s *SSSDOIDCProvider) UserInfo(ctx context.Context, accessToken string) (*v1alpha1.UserInfoResponse, error) {
	// Validate the access token and ensure it's actually an access token
	identity, err := s.jwtGenerator.ValidateTokenWithType(accessToken, "access_token")
	if err != nil {
		return &v1alpha1.UserInfoResponse{Error: lo.ToPtr("invalid_token")}, fmt.Errorf("invalid access token: %w", err)
	}

	// Get user information from SSSD
	systemUser, err := s.sssdAuthenticator.LookupUser(identity.GetUsername())
	if err != nil {
		return &v1alpha1.UserInfoResponse{Error: lo.ToPtr("invalid_token")}, fmt.Errorf("user not found: %w", err)
	}

	// Get user groups for roles
	groups, err := s.sssdAuthenticator.GetUserGroups(systemUser)
	if err != nil {
		return &v1alpha1.UserInfoResponse{Error: lo.ToPtr("server_error")}, fmt.Errorf("failed to get user groups: %w", err)
	}

	// Map groups to roles and extract organizations
	roles := s.mapGroupsToRoles(groups)
	organizations := s.extractOrganizations(groups)

	// Create user info response
	userInfo := &v1alpha1.UserInfoResponse{
		Sub:               lo.ToPtr(identity.GetUsername()),
		PreferredUsername: lo.ToPtr(identity.GetUsername()),
		Name:              lo.ToPtr(systemUser.Name),
		Email:             lo.ToPtr(""), // Email not available from system user
		EmailVerified:     lo.ToPtr(false),
		Roles:             lo.ToPtr(roles),
		Organizations:     lo.ToPtr(organizations),
	}

	return userInfo, nil
}

// GetOpenIDConfiguration returns the OpenID Connect configuration
func (s *SSSDOIDCProvider) GetOpenIDConfiguration(baseURL string) map[string]interface{} {
	// Use config values if available, otherwise use defaults
	issuer := baseURL
	if s.config != nil && s.config.Issuer != "" {
		issuer = s.config.Issuer
	}

	// Response types and grant types are determined by implementation
	responseTypes := []string{"code"}                             // Support authorization code flow
	grantTypes := []string{"authorization_code", "refresh_token"} // Support OIDC-compliant flows

	scopes := []string{"openid", "profile", "email", "roles"}
	if s.config != nil && len(s.config.Scopes) > 0 {
		scopes = s.config.Scopes
	}

	return map[string]interface{}{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/api/v1/auth/authorize",
		"token_endpoint":                        issuer + "/api/v1/auth/token",
		"userinfo_endpoint":                     issuer + "/api/v1/auth/userinfo",
		"jwks_uri":                              issuer + "/api/v1/auth/jwks",
		"response_types_supported":              responseTypes,
		"grant_types_supported":                 grantTypes,
		"scopes_supported":                      scopes,
		"claims_supported":                      []string{"sub", "preferred_username", "name", "email", "email_verified", "roles", "organizations"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_post"},
	}
}

// GetJWKS returns the JSON Web Key Set
func (s *SSSDOIDCProvider) GetJWKS() (map[string]interface{}, error) {
	// Use the JWT generator's GetJWKS method
	return s.jwtGenerator.GetJWKS()
}

// mapGroupsToRoles maps system groups to flightctl roles
// Groups starting with "org:" are treated as organizations, not roles
func (s *SSSDOIDCProvider) mapGroupsToRoles(groups []string) []string {
	var roles []string
	roleSet := make(map[string]bool) // Use set to avoid duplicates

	// Map groups to roles
	for _, group := range groups {
		// Skip organization groups (they start with "org:")
		if strings.HasPrefix(group, "org:") {
			continue
		}

		if role, exists := defaultGroupRoleMap[group]; exists {
			// Use mapped role
			if !roleSet[role] {
				roles = append(roles, role)
				roleSet[role] = true
			}
		} else {
			// Keep unmapped groups as-is (they become roles)
			if !roleSet[group] {
				roles = append(roles, group)
				roleSet[group] = true
			}
		}
	}

	return roles
}

// extractOrganizations extracts organization names from groups
// Groups starting with "org:" are treated as organizations
func (s *SSSDOIDCProvider) extractOrganizations(groups []string) []string {
	var organizations []string
	orgSet := make(map[string]bool) // Use set to avoid duplicates

	for _, group := range groups {
		if strings.HasPrefix(group, "org:") {
			// Extract organization name (remove "org:" prefix)
			orgName := strings.TrimPrefix(group, "org:")
			if orgName != "" && !orgSet[orgName] {
				organizations = append(organizations, orgName)
				orgSet[orgName] = true
			}
		}
	}

	// If no organizations found, default to "default"
	if len(organizations) == 0 {
		organizations = []string{org.DefaultExternalID}
	}

	return organizations
}

// Close closes the SSSD authenticator connection
func (s *SSSDOIDCProvider) Close() error {
	if s.sssdAuthenticator != nil {
		return s.sssdAuthenticator.Close()
	}
	return nil
}

// CleanupExpiredCodes removes expired authorization codes
func (s *SSSDOIDCProvider) CleanupExpiredCodes() {
	s.codeStore.CleanupExpiredCodes()
}

// CleanupExpiredSessions removes expired sessions
func (s *SSSDOIDCProvider) CleanupExpiredSessions() {
	s.sessionStore.CleanupExpiredSessions()
}

// IsUserAuthenticated checks if a user is authenticated via session
func (s *SSSDOIDCProvider) IsUserAuthenticated(sessionID string) (*SessionData, bool) {
	return s.sessionStore.GetSession(sessionID)
}

// CreateUserSession creates a new user session
func (s *SSSDOIDCProvider) CreateUserSession(sessionID string, username, clientID, redirectURI, state string) {
	sessionData := &SessionData{
		Username:    username,
		LoginTime:   time.Now(),
		ExpiresAt:   time.Now().Add(30 * time.Minute), // 30 minute session
		ClientID:    clientID,
		RedirectURI: redirectURI,
		State:       state,
	}
	s.sessionStore.CreateSession(sessionID, sessionData)
}

// extractSessionID extracts session ID from request context
func (s *SSSDOIDCProvider) extractSessionID(ctx context.Context, req *v1alpha1.AuthAuthorizeParams) string {
	// Extract session ID from request context
	if sessionID, ok := ctx.Value(consts.SessionIDCtxKey).(string); ok && sessionID != "" {
		return sessionID
	}

	// Check for session cookie in context
	if sessionCookie, ok := ctx.Value(consts.SessionCookieCtxKey).(string); ok && sessionCookie != "" {
		return sessionCookie
	}

	// Check for authorization header with session token
	if authHeader, ok := ctx.Value(consts.AuthorizationCtxKey).(string); ok && authHeader != "" {
		// Extract Bearer token from "Bearer <token>" format
		if strings.HasPrefix(authHeader, "Bearer ") {
			return strings.TrimPrefix(authHeader, "Bearer ")
		}
	}

	// Check for session_id query parameter
	if req.Scope != nil && *req.Scope != "" {
		// Check if the scope contains a session ID (prefixed with "session:")
		if strings.HasPrefix(*req.Scope, "session:") {
			return strings.TrimPrefix(*req.Scope, "session:")
		}
	}

	// Check for form data in context (from POST request)
	if formUsername, ok := ctx.Value(consts.FormUsernameCtxKey).(string); ok && formUsername != "" {
		// This is a login form submission, no session yet
		return ""
	}

	// No session found
	return ""
}

// LoginRequest represents a login form submission
type LoginRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	ClientID    string `json:"client_id"`
	RedirectURI string `json:"redirect_uri"`
	State       string `json:"state"`
	ReturnURL   string `json:"return_url"` // URL to return to after login
}

// LoginResponse represents the response from login
type LoginResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
	ReturnURL string `json:"return_url,omitempty"`
}

// HandleLogin processes user login and returns redirect URL
func (s *SSSDOIDCProvider) HandleLogin(ctx context.Context, req *LoginRequest) (*LoginResponse, error) {
	// Validate credentials with SSSD
	if err := s.authenticateWithSSSD(req.Username, req.Password); err != nil {
		return &LoginResponse{
			Success: false,
			Message: "Invalid username or password",
		}, nil
	}

	// In a real implementation, you would:
	// 1. Create a session for the user
	// 2. Set session cookie
	// 3. Redirect back to authorization endpoint

	// For this demo, we'll return the authorization URL
	authURL := fmt.Sprintf("/auth/authorize?response_type=code&client_id=%s&redirect_uri=%s&state=%s&scope=%s",
		req.ClientID,
		req.RedirectURI,
		req.State,
		req.Username, // Using username as scope for demo
	)

	return &LoginResponse{
		Success:   true,
		ReturnURL: authURL,
	}, nil
}

// GetLoginForm returns the HTML for the login form
func (s *SSSDOIDCProvider) GetLoginForm(clientID, redirectURI, state string) string {
	return fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
    <title>Flightctl Login</title>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <style>
        body { 
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            max-width: 400px; 
            margin: 50px auto; 
            padding: 20px;
            background-color: #f5f5f5;
        }
        .login-container {
            background: white;
            padding: 30px;
            border-radius: 8px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
        }
        .form-group { margin-bottom: 20px; }
        label { 
            display: block; 
            margin-bottom: 8px; 
            font-weight: 500;
            color: #333;
        }
        input[type="text"], input[type="password"] { 
            width: 100%%; 
            padding: 12px; 
            border: 1px solid #ddd;
            border-radius: 4px;
            font-size: 16px;
            box-sizing: border-box;
        }
        input[type="text"]:focus, input[type="password"]:focus {
            outline: none;
            border-color: #007bff;
            box-shadow: 0 0 0 2px rgba(0,123,255,0.25);
        }
        button { 
            background: #007bff; 
            color: white; 
            padding: 12px 24px; 
            border: none; 
            border-radius: 4px;
            cursor: pointer; 
            font-size: 16px;
            width: 100%%;
            transition: background-color 0.2s;
        }
        button:hover { background: #0056b3; }
        button:active { background: #004085; }
        .error { 
            color: #dc3545; 
            margin-top: 10px; 
            padding: 8px;
            background: #f8d7da;
            border: 1px solid #f5c6cb;
            border-radius: 4px;
            display: none;
        }
        .header {
            text-align: center;
            margin-bottom: 30px;
        }
        .header h1 {
            color: #333;
            margin: 0;
            font-size: 24px;
        }
        .header p {
            color: #666;
            margin: 5px 0 0 0;
            font-size: 14px;
        }
    </style>
    <script>
        function handleSubmit(event) {
            event.preventDefault();
            const form = event.target;
            const formData = new FormData(form);
            
            // Show loading state
            const button = form.querySelector('button');
            const originalText = button.textContent;
            button.textContent = 'Logging in...';
            button.disabled = true;
            
            // Submit form data
            fetch('/auth/login', {
                method: 'POST',
                body: formData
            })
            .then(response => {
                if (response.ok) {
                    // Parse the redirect URL from response
                    return response.text();
                } else {
                    throw new Error('Login failed');
                }
            })
            .then(data => {
                // If we get a redirect URL, follow it
                if (data.startsWith('http') || data.startsWith('/')) {
                    window.location.href = data;
                } else {
                    // Otherwise, it might be an error message
                    showError(data);
                }
            })
            .catch(error => {
                showError('Login failed. Please check your credentials.');
            })
            .finally(() => {
                button.textContent = originalText;
                button.disabled = false;
            });
        }
        
        function showError(message) {
            const errorDiv = document.querySelector('.error');
            errorDiv.textContent = message;
            errorDiv.style.display = 'block';
        }
    </script>
</head>
<body>
    <div class="login-container">
        <div class="header">
            <h1>Flightctl</h1>
            <p>Please sign in to continue</p>
        </div>
        
        <form onsubmit="handleSubmit(event)">
            <input type="hidden" name="response_type" value="code">
            <input type="hidden" name="client_id" value="%s">
            <input type="hidden" name="redirect_uri" value="%s">
            <input type="hidden" name="state" value="%s">
            
            <div class="form-group">
                <label for="username">Username:</label>
                <input type="text" id="username" name="scope" required autocomplete="username">
            </div>
            
            <div class="form-group">
                <label for="password">Password:</label>
                <input type="password" id="password" name="password" required autocomplete="current-password">
            </div>
            
            <button type="submit">Sign In</button>
            
            <div class="error"></div>
        </form>
    </div>
</body>
</html>`, clientID, redirectURI, state)
}
