//go:build linux

package pam

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"sync"
	"time"

	pamapi "github.com/flightctl/flightctl/api/v1alpha1/pam-issuer"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// Default group to role mapping
var defaultGroupRoleMap = map[string]string{
	"flightctl-admin":     "admin",
	"flightctl-operator":  "operator",
	"flightctl-viewer":    "viewer",
	"flightctl-installer": "installer",
	"wheel":               "admin",    // Traditional Unix admin group
	"sudo":                "admin",    // Sudo users get admin access
	"adm":                 "operator", // System administration group
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
		code, ok := key.(string)
		if !ok {
			return true
		}

		codeData, ok := value.(*AuthorizationCodeData)
		if !ok {
			s.codes.Delete(code)
			return true
		}

		if now.After(codeData.ExpiresAt) {
			s.codes.Delete(code)
		}
		return true
	})
}

// generateSessionID generates a cryptographically secure session ID
// Used for tracking authenticated browser sessions (stored in cookies)
func generateSessionID() (string, error) {
	bytes := make([]byte, 32) // 256 bits of entropy
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// generateAuthorizationCode generates a cryptographically secure OAuth2 authorization code
// Used in the authorization code flow, exchanged for access tokens at the /token endpoint
func generateAuthorizationCode() (string, error) {
	bytes := make([]byte, 32) // 256 bits of entropy
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// PAMOIDCProvider handles OIDC-compliant authentication flows using PAM/NSS

// NewPAMOIDCProvider creates a new PAM-based OIDC provider
func NewPAMOIDCProvider(caClient *fccrypto.CAClient, config *config.PAMOIDCIssuer) (*PAMOIDCProvider, error) {
	return NewPAMOIDCProviderWithAuthenticator(caClient, config, nil)
}

// NewPAMOIDCProviderWithAuthenticator creates a new PAM-based OIDC provider with a custom authenticator
func NewPAMOIDCProviderWithAuthenticator(caClient *fccrypto.CAClient, config *config.PAMOIDCIssuer, pamAuth Authenticator) (*PAMOIDCProvider, error) {
	jwtGen, err := authn.NewJWTGenerator(caClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT generator: %w", err)
	}

	// Create default authenticator if none provided
	if pamAuth == nil {
		pamAuth, err = NewPAMAuthenticator()
		if err != nil {
			return nil, fmt.Errorf("failed to create PAM authenticator: %w", err)
		}
	}

	return &PAMOIDCProvider{
		jwtGenerator:     jwtGen,
		config:           config,
		pamAuthenticator: pamAuth,
		codeStore:        NewAuthorizationCodeStore(),
		sessionStore:     NewSessionStore(),
		log:              logrus.New(),
	}, nil
}

// Token implements OIDCProvider interface - handles OAuth2 token requests
func (s *PAMOIDCProvider) Token(ctx context.Context, req *pamapi.TokenRequest) (*pamapi.TokenResponse, error) {
	// Handle different grant types - only OIDC-compliant flows
	switch req.GrantType {
	case pamapi.RefreshToken:
		return s.handleRefreshTokenGrant(ctx, req)
	case pamapi.AuthorizationCode:
		return s.handleAuthorizationCodeGrant(ctx, req)
	default:
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorUnsupportedGrantType)}, nil
	}
}

// handleRefreshTokenGrant handles the refresh_token grant type
func (s *PAMOIDCProvider) handleRefreshTokenGrant(ctx context.Context, req *pamapi.TokenRequest) (*pamapi.TokenResponse, error) {
	// Validate required fields for refresh token flow
	if req.RefreshToken == nil || *req.RefreshToken == "" {
		s.log.Errorf("handleRefreshTokenGrant: missing refresh token")
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidRequest)}, nil
	}

	// Validate the refresh token and ensure it's actually a refresh token
	identity, err := s.jwtGenerator.ValidateTokenWithType(*req.RefreshToken, TokenTypeRefresh)
	if err != nil {
		s.log.Errorf("handleRefreshTokenGrant: failed to validate refresh token - %v", err)
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidGrant)}, nil
	}

	// Get current user information from NSS to ensure user still exists
	systemUser, err := s.pamAuthenticator.LookupUser(identity.GetUsername())
	if err != nil {
		s.log.Errorf("handleRefreshTokenGrant: failed to lookup user - %v", err)
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidGrant)}, nil
	}

	// Get current user groups for roles
	groups, err := s.pamAuthenticator.GetUserGroups(systemUser)
	if err != nil {
		s.log.Errorf("handleRefreshTokenGrant: failed to get user groups - %v", err)
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorServerError)}, nil
	}

	// Map groups to roles and extract organizations
	roles := s.mapGroupsToRoles(groups)
	s.log.Debugf("handleRefreshTokenGrant: mapped groups to roles - %v", roles)
	organizations := s.extractOrganizations(groups)
	s.log.Debugf("handleRefreshTokenGrant: extracted organizations - %v", organizations)
	tokenGenerationRequest := authn.TokenGenerationRequest{
		Username:      identity.GetUsername(),
		UID:           identity.GetUID(),
		Organizations: organizations,
		Roles:         roles,
		Audience:      []string{s.config.ClientID}, // Set audience to client ID
		Issuer:        s.config.Issuer,             // Set issuer
	}
	// Generate new access token with proper expiry (1 hour)
	accessToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, time.Hour, TokenTypeAccess)
	if err != nil {
		s.log.Errorf("handleRefreshTokenGrant: server error when generating access token - %v", err)
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorServerError)}, nil
	}

	// Create token response
	tokenResponse := &pamapi.TokenResponse{
		AccessToken: lo.ToPtr(accessToken),
		TokenType:   lo.ToPtr(pamapi.Bearer),
		ExpiresIn:   lo.ToPtr(int(time.Hour.Seconds())),
	}

	// Always issue a new refresh token when using refresh_token grant
	// (if we have a refresh token, it means offline_access was originally granted)
	refreshToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, 7*24*time.Hour, TokenTypeRefresh)
	if err != nil {
		s.log.Errorf("handleRefreshTokenGrant: server error when generating refresh token - %v", err)
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorServerError)}, nil
	}
	tokenResponse.RefreshToken = lo.ToPtr(refreshToken)

	return tokenResponse, nil
}

// handleAuthorizationCodeGrant handles the authorization_code grant type
func (s *PAMOIDCProvider) handleAuthorizationCodeGrant(ctx context.Context, req *pamapi.TokenRequest) (*pamapi.TokenResponse, error) {
	// Validate required fields for authorization code flow
	if req.Code == nil || *req.Code == "" {
		s.log.Warnf("handleAuthorizationCodeGrant: missing authorization code")
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidRequest)}, nil
	}

	// Validate client ID
	if req.ClientId == nil || *req.ClientId == "" {
		s.log.Warnf("handleAuthorizationCodeGrant: missing client ID")
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidClient)}, nil
	}
	if s.config == nil || s.config.ClientID != *req.ClientId {
		s.log.Warnf("handleAuthorizationCodeGrant: invalid client ID - expected=%s, got=%s", s.config.ClientID, *req.ClientId)
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidClient)}, nil
	}

	// Validate client authentication based on whether a secret is configured
	// If clientSecret is configured (non-empty), this is a confidential client and we require authentication
	// If clientSecret is empty, this is a public client (CLI, SPA) and we don't require secret (should use PKCE in production)
	if s.config.ClientSecret != "" {
		// Confidential client - require client_secret_post authentication
		if req.ClientSecret == nil {
			s.log.Errorf("handleAuthorizationCodeGrant: missing client secret")
			return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidClient)}, nil
		}
		if *req.ClientSecret != s.config.ClientSecret {
			s.log.Errorf("handleAuthorizationCodeGrant: invalid client secret")
			return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidClient)}, nil
		}
	}
	// For public clients (empty secret), we accept the request without secret validation
	// In production, this should be combined with PKCE (code_challenge/code_verifier) for security

	// Validate and retrieve authorization code
	codeData, exists := s.codeStore.GetCode(*req.Code)
	if !exists {
		s.log.Warnf("handleAuthorizationCodeGrant: authorization code not found")
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidGrant)}, nil
	}

	// Validate that the client ID matches the stored code
	if codeData.ClientID != *req.ClientId {
		s.log.Warnf("handleAuthorizationCodeGrant: invalid client ID - expected=%s, got=%s", codeData.ClientID, *req.ClientId)
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidGrant)}, nil
	}

	// Get user information from NSS
	systemUser, err := s.pamAuthenticator.LookupUser(codeData.Username)
	if err != nil {
		s.log.Errorf("handleAuthorizationCodeGrant: failed to lookup user - %v", err)
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorInvalidGrant)}, nil
	}

	// Get user groups for roles
	groups, err := s.pamAuthenticator.GetUserGroups(systemUser)
	if err != nil {
		s.log.Errorf("handleAuthorizationCodeGrant: failed to get user groups - %v", err)
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorServerError)}, nil
	}

	// Map groups to roles and extract organizations
	roles := s.mapGroupsToRoles(groups)
	s.log.Debugf("handleAuthorizationCodeGrant: mapped groups to roles - %v", roles)
	organizations := s.extractOrganizations(groups)
	s.log.Debugf("handleAuthorizationCodeGrant: extracted organizations - %v", organizations)

	// Create identity for token generation
	tokenGenerationRequest := authn.TokenGenerationRequest{
		Username:      codeData.Username,
		UID:           codeData.Username,
		Organizations: organizations,
		Roles:         roles,
		Audience:      []string{codeData.ClientID}, // Set audience to client ID from authorization request
		Issuer:        s.config.Issuer,             // Set issuer
	}
	// Generate access token with proper expiry (1 hour)
	accessToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, time.Hour, TokenTypeAccess)
	if err != nil {
		s.log.Errorf("handleAuthorizationCodeGrant: server error when generating access token - %v", err)
		return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorServerError)}, nil
	}

	// Create token response
	tokenResponse := &pamapi.TokenResponse{
		AccessToken: lo.ToPtr(accessToken),
		TokenType:   lo.ToPtr(pamapi.Bearer),
		ExpiresIn:   lo.ToPtr(int(time.Hour.Seconds())),
	}

	// Only generate refresh token if offline_access was requested
	if strings.Contains(codeData.Scope, ScopeOfflineAccess) {
		refreshToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, 7*24*time.Hour, TokenTypeRefresh)
		if err != nil {
			s.log.Errorf("handleAuthorizationCodeGrant: server error when generating refresh token - %v", err)
			return &pamapi.TokenResponse{Error: lo.ToPtr(ErrorServerError)}, nil
		}
		tokenResponse.RefreshToken = lo.ToPtr(refreshToken)
	}

	return tokenResponse, nil
}

// Authorize handles the authorization endpoint for authorization code flow
func (s *PAMOIDCProvider) Authorize(ctx context.Context, req *pamapi.AuthAuthorizeParams) (*AuthorizeResponse, error) {
	s.log.Debugf("Authorize: starting authorization request - ClientId=%s, RedirectUri=%s, ResponseType=%s, Scope=%s, State=%s",
		req.ClientId, req.RedirectUri, req.ResponseType, lo.FromPtrOr(req.Scope, ""), lo.FromPtrOr(req.State, ""))

	// Validate required fields
	if req.ClientId == "" || req.RedirectUri == "" {
		s.log.Warnf("Authorize: missing required fields - ClientId=%s, RedirectUri=%s", req.ClientId, req.RedirectUri)
		return nil, errors.New(ErrorInvalidRequest)
	}

	// Validate client ID
	if s.config == nil {
		s.log.Errorf("Authorize: config is nil")
		return nil, errors.New(ErrorInvalidClient)
	}
	if s.config.ClientID != req.ClientId {
		s.log.Warnf("Authorize: invalid client ID - expected=%s, got=%s", s.config.ClientID, req.ClientId)
		return nil, errors.New(ErrorInvalidClient)
	}
	s.log.Debugf("Authorize: client ID validation passed - %s", req.ClientId)

	// Validate redirect URI
	validRedirect := false
	if s.config != nil {
		s.log.Debugf("Authorize: checking redirect URI - requested=%s, allowed=%v", req.RedirectUri, s.config.RedirectURIs)
		for _, uri := range s.config.RedirectURIs {
			if uri == req.RedirectUri {
				validRedirect = true
				s.log.Debugf("Authorize: redirect URI validation passed - %s", req.RedirectUri)
				break
			}
		}
	}
	if !validRedirect {
		s.log.Warnf("Authorize: invalid redirect URI - %s", req.RedirectUri)
		return nil, errors.New(ErrorInvalidRequest)
	}

	// Validate response type
	if req.ResponseType != pamapi.Code {
		s.log.Warnf("Authorize: unsupported response type - %s", req.ResponseType)
		return nil, errors.New(ErrorUnsupportedGrantType)
	}
	s.log.Debugf("Authorize: response type validation passed - %s", req.ResponseType)

	// Authorization flow:
	// 1. Check if user is already authenticated (session/cookie)
	// 2. If not authenticated, return embedded login form
	// 3. User submits credentials via POST to /auth/login
	// 4. Server validates with PAM and generates authorization code
	// 5. Server redirects back to client with code

	// Extract session ID from request context
	sessionID := s.extractSessionID(ctx, req)
	s.log.Debugf("Authorize: extracted session ID")

	// Check if user is already authenticated via session
	if sessionID == "" {
		s.log.Debugf("Authorize: no session ID found, returning login form")
		// User not authenticated, return embedded login form
		loginForm := s.GetLoginForm(req.ClientId, req.RedirectUri, lo.FromPtrOr(req.State, ""))
		return &AuthorizeResponse{
			Type:    AuthorizeResponseTypeHTML,
			Content: loginForm,
		}, nil
	}

	// Check if session exists and is valid
	sessionData, exists := s.IsUserAuthenticated(sessionID)
	if !exists {
		s.log.Debugf("Authorize: session not found or expired, returning login form")
		// Session invalid or expired, return login form
		loginForm := s.GetLoginForm(req.ClientId, req.RedirectUri, lo.FromPtrOr(req.State, ""))
		return &AuthorizeResponse{
			Type:    AuthorizeResponseTypeHTML,
			Content: loginForm,
		}, nil
	}

	// User is authenticated, get username from session
	username := sessionData.Username
	s.log.Infof("Authorize: user authenticated via session - username=%s", username)

	// SECURITY: Validate that the current request matches the session to prevent CSRF attacks
	// The session was created when the user authenticated with specific client_id, redirect_uri, and state
	// These must match the current authorize request to ensure this is the same OAuth flow
	if sessionData.ClientID != req.ClientId {
		s.log.Warnf("Authorize: client_id mismatch - session=%s, request=%s", sessionData.ClientID, req.ClientId)
		return nil, errors.New(ErrorInvalidRequest)
	}
	if sessionData.RedirectURI != req.RedirectUri {
		s.log.Warnf("Authorize: redirect_uri mismatch - session=%s, request=%s", sessionData.RedirectURI, req.RedirectUri)
		return nil, errors.New(ErrorInvalidRequest)
	}
	if sessionData.State != lo.FromPtrOr(req.State, "") {
		s.log.Warnf("Authorize: state mismatch - session=%s, request=%s", sessionData.State, lo.FromPtrOr(req.State, ""))
		return nil, errors.New(ErrorInvalidRequest)
	}

	// Generate OAuth2 authorization code (step 4: used to exchange for access token)
	authCode, err := generateAuthorizationCode()
	if err != nil {
		s.log.Errorf("Authorize: failed to generate authorization code - %v", err)
		return nil, errors.New(ErrorServerError)
	}
	s.log.Debugf("Authorize: generated authorization code")

	// Use the requested scope if provided, otherwise determine based on user's role/group membership
	scopes := lo.FromPtrOr(req.Scope, s.determineUserScopes(username))
	s.log.Debugf("Authorize: determined scopes - %s", scopes)

	// Store authorization code with expiration (10 minutes)
	// Use values from session (which were validated above) to prevent parameter tampering
	codeData := &AuthorizationCodeData{
		Code:        authCode,
		ClientID:    sessionData.ClientID,
		RedirectURI: sessionData.RedirectURI,
		Scope:       scopes,
		State:       sessionData.State,
		Username:    username,
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		CreatedAt:   time.Now(),
	}

	s.codeStore.StoreCode(codeData)
	s.log.Debugf("Authorize: stored authorization code for user - %s", username)

	// Build redirect URL with authorization code
	// Use sessionData.RedirectURI (already validated above) instead of req.RedirectUri
	parsed, err := url.Parse(sessionData.RedirectURI)
	if err != nil {
		s.log.Errorf("Authorize: failed to parse redirect URI - %v", err)
		return nil, fmt.Errorf("invalid redirect URI: %w", err)
	}

	values := parsed.Query()
	values.Set("code", authCode)
	// Use sessionData.State (already validated above) instead of req.State
	if sessionData.State != "" {
		values.Set("state", sessionData.State)
	}
	parsed.RawQuery = values.Encode()
	redirectURL := parsed.String()

	s.log.Debugf("Authorize: returning redirect")
	return &AuthorizeResponse{
		Type:    AuthorizeResponseTypeRedirect,
		Content: redirectURL,
	}, nil
}

// Login handles the login form submission
func (s *PAMOIDCProvider) Login(ctx context.Context, username, password, clientID, redirectURI, state string) (*LoginResult, error) {
	s.log.Debugf("Login: attempting authentication for user %s", username)

	// Validate credentials with PAM/NSS
	s.log.Debugf("Login: calling PAM authentication for user %s", username)
	if err := s.authenticateWithPAM(username, password); err != nil {
		s.log.Errorf("Login: PAM authentication failed for user %s - %v", username, err)
		return nil, errors.New(ErrorInvalidGrant)
	}
	s.log.Infof("Login: PAM authentication successful for user %s", username)

	// User is authenticated, create session (step 2: stored in cookie for subsequent authorize call)
	sessionID, err := generateSessionID()
	if err != nil {
		s.log.Errorf("Login: failed to generate session ID for user %s - %v", username, err)
		return nil, errors.New(ErrorServerError)
	}
	s.log.Debugf("Login: generated session ID for user %s", username)

	// Create user session
	s.CreateUserSession(sessionID, username, clientID, redirectURI, state)
	s.log.Debugf("Login: created user session for %s", username)

	// Redirect back to authorization endpoint without session ID in URL
	authURL := fmt.Sprintf("/api/v1/auth/authorize?response_type=code&client_id=%s&redirect_uri=%s", clientID, redirectURI)
	if state != "" {
		authURL += fmt.Sprintf("&state=%s", state)
	}

	s.log.Debugf("Login: returning redirect for user %s", username)
	return &LoginResult{
		RedirectURL: authURL,
		SessionID:   sessionID,
	}, nil
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
		sessionID, ok := key.(string)
		if !ok {
			return true
		}

		sessionData, ok := value.(*SessionData)
		if !ok {
			s.sessions.Delete(sessionID)
			return true
		}

		if now.After(sessionData.ExpiresAt) {
			s.sessions.Delete(sessionID)
		}
		return true
	})
}

// authenticateWithPAM authenticates a user using PAM/NSS
func (s *PAMOIDCProvider) authenticateWithPAM(username, password string) error {
	return s.pamAuthenticator.Authenticate(username, password)
}

// determineUserScopes determines the scopes to grant - just the standard OIDC scopes
func (s *PAMOIDCProvider) determineUserScopes(username string) string {
	// Standard OIDC scopes for all authenticated users
	return DefaultScopes
}

// UserInfo implements OIDCProvider interface - returns user information
func (s *PAMOIDCProvider) UserInfo(ctx context.Context, accessToken string) (*pamapi.UserInfoResponse, error) {
	// Validate the access token and ensure it's actually an access token
	identity, err := s.jwtGenerator.ValidateTokenWithType(accessToken, TokenTypeAccess)
	if err != nil {
		return &pamapi.UserInfoResponse{Error: lo.ToPtr(ErrorInvalidToken)}, fmt.Errorf("invalid access token: %w", err)
	}

	// Get user information from NSS
	systemUser, err := s.pamAuthenticator.LookupUser(identity.GetUsername())
	if err != nil {
		return &pamapi.UserInfoResponse{Error: lo.ToPtr(ErrorInvalidToken)}, fmt.Errorf("user not found: %w", err)
	}

	// Get user groups for roles
	groups, err := s.pamAuthenticator.GetUserGroups(systemUser)
	if err != nil {
		return &pamapi.UserInfoResponse{Error: lo.ToPtr(ErrorServerError)}, fmt.Errorf("failed to get user groups: %w", err)
	}

	// Map groups to roles and extract organizations
	roles := s.mapGroupsToRoles(groups)
	organizations := s.extractOrganizations(groups)

	// Create user info response
	userInfo := &pamapi.UserInfoResponse{
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
func (s *PAMOIDCProvider) GetOpenIDConfiguration() (*pamapi.OpenIDConfiguration, error) {
	// Use issuer from config
	if s.config == nil || s.config.Issuer == "" {
		return nil, fmt.Errorf("issuer URL not configured")
	}
	issuer := s.config.Issuer

	// Response types and grant types are determined by implementation
	responseTypes := []string{"code"}                             // Support authorization code flow
	grantTypes := []string{"authorization_code", "refresh_token"} // Support OIDC-compliant flows

	scopes := []string{"openid", "profile", "email", "roles"}
	if s.config != nil && len(s.config.Scopes) > 0 {
		scopes = s.config.Scopes
	}

	// Get the signing algorithms from all keys in the JWKS
	// This is dynamic based on the actual key types (RSA=RS256, EC=ES256)
	// Supports key rotation scenarios where multiple keys may be active
	// Per OIDC Discovery spec, id_token_signing_alg_values_supported is REQUIRED
	jwks, err := s.jwtGenerator.GetJWKS()
	if err != nil {
		return nil, fmt.Errorf("failed to get JWKS: %w", err)
	}
	if jwks.Keys == nil || len(*jwks.Keys) == 0 {
		return nil, fmt.Errorf("JWKS contains no keys")
	}

	// Collect all unique algorithms from all keys
	algSet := make(map[string]struct{})
	for _, key := range *jwks.Keys {
		if key.Alg == nil {
			continue
		}
		algSet[*key.Alg] = struct{}{}
	}
	if len(algSet) == 0 {
		return nil, fmt.Errorf("no valid signing algorithms found in JWKS")
	}

	// Convert set to slice
	idTokenSigningAlgs := make([]string, 0, len(algSet))
	for alg := range algSet {
		idTokenSigningAlgs = append(idTokenSigningAlgs, alg)
	}

	authzEndpoint := issuer + "/authorize"
	tokenEndpoint := issuer + "/token"
	userinfoEndpoint := issuer + "/userinfo"
	jwksURI := issuer + "/jwks"
	claimsSupported := []string{"sub", "preferred_username", "name", "email", "email_verified", "roles", "organizations"}
	subjectTypesSupported := []pamapi.OpenIDConfigurationSubjectTypesSupported{pamapi.Public}
	// Determine supported client authentication methods based on configuration
	// - "none": For public clients (CLI, SPA) when no client secret is configured
	// - "client_secret_post": For confidential clients (backend services) when client secret is configured
	tokenEndpointAuthMethods := []string{AuthMethodNone} // Default: support public clients
	if s.config != nil && s.config.ClientSecret != "" {
		// If a client secret is configured, we support both public and confidential clients
		tokenEndpointAuthMethods = []string{AuthMethodNone, AuthMethodClientSecretPost}
	}

	return &pamapi.OpenIDConfiguration{
		Issuer:                            &issuer,
		AuthorizationEndpoint:             &authzEndpoint,
		TokenEndpoint:                     &tokenEndpoint,
		UserinfoEndpoint:                  &userinfoEndpoint,
		JwksUri:                           &jwksURI,
		ResponseTypesSupported:            &responseTypes,
		GrantTypesSupported:               &grantTypes,
		ScopesSupported:                   &scopes,
		ClaimsSupported:                   &claimsSupported,
		IdTokenSigningAlgValuesSupported:  &idTokenSigningAlgs,
		TokenEndpointAuthMethodsSupported: &tokenEndpointAuthMethods,
		SubjectTypesSupported:             &subjectTypesSupported,
	}, nil
}

// GetJWKS returns the JSON Web Key Set
func (s *PAMOIDCProvider) GetJWKS() (*pamapi.JWKSResponse, error) {
	// Use the JWT generator's GetJWKS method
	return s.jwtGenerator.GetJWKS()
}

// mapGroupsToRoles maps system groups to flightctl roles
// Groups starting with "org:" are treated as organizations, not roles
func (s *PAMOIDCProvider) mapGroupsToRoles(groups []string) []string {
	var roles []string
	roleSet := make(map[string]struct{}) // Use set to avoid duplicates

	// Map groups to roles
	for _, group := range groups {
		// Skip organization groups (they start with "org:")
		if strings.HasPrefix(group, OrgPrefix) {
			continue
		}

		if role, exists := defaultGroupRoleMap[group]; exists {
			// Use mapped role
			if _, exists := roleSet[role]; !exists {
				roles = append(roles, role)
				roleSet[role] = struct{}{}
			}
		} else {
			// Keep unmapped groups as-is (they become roles)
			if _, exists := roleSet[group]; !exists {
				roles = append(roles, group)
				roleSet[group] = struct{}{}
			}
		}
	}

	return roles
}

// extractOrganizations extracts organization names from groups
// Groups starting with "org:" are treated as organizations
func (s *PAMOIDCProvider) extractOrganizations(groups []string) []string {
	var organizations []string
	orgSet := make(map[string]struct{}) // Use set to avoid duplicates

	for _, group := range groups {
		if strings.HasPrefix(group, OrgPrefix) {
			// Extract organization name (remove "org:" prefix)
			orgName := strings.TrimPrefix(group, OrgPrefix)
			if _, exists := orgSet[orgName]; orgName != "" && !exists {
				organizations = append(organizations, orgName)
				orgSet[orgName] = struct{}{}
			}
		}
	}

	// If no organizations found, default to "default"
	if len(organizations) == 0 {
		organizations = []string{"default"}
	}

	return organizations
}

// Close closes the PAM authenticator connection
func (s *PAMOIDCProvider) Close() error {
	if s.pamAuthenticator != nil {
		return s.pamAuthenticator.Close()
	}
	return nil
}

// CleanupExpiredCodes removes expired authorization codes
func (s *PAMOIDCProvider) CleanupExpiredCodes() {
	s.codeStore.CleanupExpiredCodes()
}

// CleanupExpiredSessions removes expired sessions
func (s *PAMOIDCProvider) CleanupExpiredSessions() {
	s.sessionStore.CleanupExpiredSessions()
}

// IsUserAuthenticated checks if a user is authenticated via session
func (s *PAMOIDCProvider) IsUserAuthenticated(sessionID string) (*SessionData, bool) {
	return s.sessionStore.GetSession(sessionID)
}

// CreateUserSession creates a new user session
func (s *PAMOIDCProvider) CreateUserSession(sessionID string, username, clientID, redirectURI, state string) {
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
func (s *PAMOIDCProvider) extractSessionID(ctx context.Context, req *pamapi.AuthAuthorizeParams) string {
	// Check for session cookie in context (standard OAuth2/OIDC session management)
	if sessionCookie, ok := ctx.Value(SessionCookieCtxKey).(string); ok && sessionCookie != "" {
		return sessionCookie
	}

	// No session found
	return ""
}

// GetLoginForm returns the HTML for the login form
func (s *PAMOIDCProvider) GetLoginForm(clientID, redirectURI, state string) string {
	// Escape all variables to prevent XSS attacks
	escClientID := html.EscapeString(clientID)
	escRedirectURI := html.EscapeString(redirectURI)
	escState := html.EscapeString(state)

	return fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
    <title>Flight Control Login</title>
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
            
            // Convert FormData to URLSearchParams for application/x-www-form-urlencoded
            const params = new URLSearchParams(formData);
            
            // Submit form data
            fetch('/api/v1/auth/login', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/x-www-form-urlencoded'
                },
                body: params
            })
            .then(response => {
                // Read the response text for both success and error
                return response.text().then(text => ({
                    ok: response.ok,
                    text: text
                }));
            })
            .then(result => {
                if (result.ok) {
                    // Successful login - redirect to the URL
                    window.location.href = result.text;
                } else {
                    // Failed login - show error message from server
                    showError(result.text);
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
            <h1>Flight Control</h1>
            <p>Please sign in to continue</p>
        </div>
        
        <form onsubmit="handleSubmit(event)">
            <input type="hidden" name="response_type" value="code">
            <input type="hidden" name="client_id" value="%s">
            <input type="hidden" name="redirect_uri" value="%s">
            <input type="hidden" name="state" value="%s">
            
            <div class="form-group">
                <label for="username">Username:</label>
                <input type="text" id="username" name="username" required autocomplete="username">
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
</html>`, escClientID, escRedirectURI, escState)
}
