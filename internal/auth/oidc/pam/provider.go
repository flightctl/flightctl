//go:build linux

package pam

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	pamapi "github.com/flightctl/flightctl/api/v1beta1/pam-issuer"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

//go:embed templates/login_form.html
var loginFormTemplate string

//go:embed templates/login_form_error.html
var loginFormErrorHTML string

// LoginFormData represents the data used to populate the login form template
// Currently empty as all authorization parameters are stored in encrypted cookie
type LoginFormData struct {
}

// EncryptedAuthData represents encrypted authorization/session data stored in cookie
// When IsLoggedIn is false (or Username is empty), it represents a pending authorization request
// When IsLoggedIn is true and Username is set, it represents an authenticated session
type EncryptedAuthData struct {
	// Common fields for both pending auth and authenticated sessions
	ClientID            string
	RedirectURI         string
	Scope               string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           int64 // Unix timestamp

	// Session-specific fields (only set when IsLoggedIn is true)
	Username   string
	IsLoggedIn bool
	LoginTime  int64 // Unix timestamp (only set when IsLoggedIn is true)
}

// AuthorizationCodeData represents stored authorization code data
type AuthorizationCodeData struct {
	Code                string
	ClientID            string
	RedirectURI         string
	Scope               string
	State               string
	Username            string
	ExpiresAt           time.Time
	CreatedAt           time.Time
	CodeChallenge       string                                        // PKCE code challenge
	CodeChallengeMethod pamapi.AuthAuthorizeParamsCodeChallengeMethod // PKCE code challenge method (plain or S256)
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

// generateAuthorizationCode generates a cryptographically secure OAuth2 authorization code
// Used in the authorization code flow, exchanged for access tokens at the /token endpoint
func generateAuthorizationCode() (string, error) {
	bytes := make([]byte, 32) // 256 bits of entropy
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// verifyPKCEChallenge verifies that the code_verifier matches the stored code_challenge
// according to RFC 7636 (Proof Key for Code Exchange)
// Only S256 method is supported (plain is not secure and not allowed)
// Assumes both codeChallenge and codeVerifier are non-empty (enforced by caller)
func verifyPKCEChallenge(codeVerifier, codeChallenge string, codeChallengeMethod pamapi.AuthAuthorizeParamsCodeChallengeMethod) bool {
	if codeVerifier == "" || codeChallenge == "" {
		// Both must be provided
		return false
	}

	// S256: BASE64URL(SHA256(ASCII(code_verifier)))
	hash := sha256.Sum256([]byte(codeVerifier))
	computedChallenge := base64.RawURLEncoding.EncodeToString(hash[:])
	return computedChallenge == codeChallenge
}

// deriveCookieEncryptionKey derives a 32-byte AES-256 key from the CA private key
func deriveCookieEncryptionKey(caClient *fccrypto.CAClient) ([]byte, error) {
	// Get CA private key file path
	caConfig := caClient.Config()
	if caConfig == nil || caConfig.InternalConfig == nil {
		return nil, fmt.Errorf("CA configuration not available")
	}

	caKeyFile := fccrypto.CertStorePath(caConfig.InternalConfig.KeyFile, caConfig.InternalConfig.CertStore)
	keyBytes, err := os.ReadFile(caKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA key file: %w", err)
	}

	// Use HKDF to derive a symmetric key from CA key
	// Salt: "flightctl-cookie-encryption" (application-specific)
	// Info: empty (no additional context needed)
	// KeyLength: 32 bytes for AES-256
	key, err := hkdf.Key(sha256.New, keyBytes, []byte("flightctl-cookie-encryption"), "", 32)
	if err != nil {
		return nil, fmt.Errorf("failed to derive key: %w", err)
	}

	return key, nil
}

// EncryptCookieData encrypts the auth data using AES-256-GCM
// This is used to store authorization parameters (pending or authenticated) in a secure cookie
func (s *PAMOIDCProvider) EncryptCookieData(data *EncryptedAuthData) (string, error) {
	// Marshal to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal data: %w", err)
	}

	// Create AES cipher
	block, err := aes.NewCipher(s.cookieKey)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	// Create GCM
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt
	ciphertext := gcm.Seal(nonce, nonce, jsonData, nil)

	// Encode to base64 for cookie
	return base64.URLEncoding.EncodeToString(ciphertext), nil
}

// DecryptCookieData decrypts the auth data from cookie
// Returns the decrypted data which may represent either a pending auth request or authenticated session
func (s *PAMOIDCProvider) DecryptCookieData(encrypted string) (*EncryptedAuthData, error) {
	// Decode from base64
	ciphertext, err := base64.URLEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64: %w", err)
	}

	// Create AES cipher
	block, err := aes.NewCipher(s.cookieKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Create GCM
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Extract nonce
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	// Decrypt
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	// Unmarshal JSON
	var data EncryptedAuthData
	if err := json.Unmarshal(plaintext, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal data: %w", err)
	}

	// Check expiration
	if time.Now().Unix() > data.ExpiresAt {
		return nil, fmt.Errorf("cookie expired")
	}

	return &data, nil
}

// EncryptSessionData is a convenience method that encrypts session data
// It's an alias for EncryptCookieData but with a clearer name for session data
func (s *PAMOIDCProvider) EncryptSessionData(data *EncryptedAuthData) (string, error) {
	return s.EncryptCookieData(data)
}

// DecryptSessionData is a convenience method that decrypts session data
// It's an alias for DecryptCookieData but with a clearer name for session data
func (s *PAMOIDCProvider) DecryptSessionData(encrypted string) (*EncryptedAuthData, error) {
	return s.DecryptCookieData(encrypted)
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

	// Derive cookie encryption key from CA key
	cookieKey, err := deriveCookieEncryptionKey(caClient)
	if err != nil {
		return nil, fmt.Errorf("failed to derive cookie encryption key: %w", err)
	}

	// Create default authenticator if none provided
	if pamAuth == nil {
		pamAuth, err = NewPAMAuthenticator()
		if err != nil {
			return nil, fmt.Errorf("failed to create PAM authenticator: %w", err)
		}
	}

	loginFormTmpl := template.Must(template.New("loginForm").Parse(loginFormTemplate))

	return &PAMOIDCProvider{
		jwtGenerator:      jwtGen,
		config:            config,
		pamAuthenticator:  pamAuth,
		codeStore:         NewAuthorizationCodeStore(),
		log:               logrus.New(),
		loginFormTemplate: loginFormTmpl,
		cookieKey:         cookieKey,
	}, nil
}

// getAccessTokenExpiration returns the access token expiration duration from config
func (s *PAMOIDCProvider) getAccessTokenExpiration() time.Duration {
	if s.config == nil {
		return time.Hour // fallback if config is nil
	}
	return time.Duration(s.config.AccessTokenExpiration)
}

// getRefreshTokenExpiration returns the refresh token expiration duration from config
func (s *PAMOIDCProvider) getRefreshTokenExpiration() time.Duration {
	if s.config == nil {
		return 7 * 24 * time.Hour // fallback if config is nil
	}
	return time.Duration(s.config.RefreshTokenExpiration)
}

// getPendingAuthExpiration returns the pending auth cookie expiration duration from config
func (s *PAMOIDCProvider) getPendingAuthExpiration() time.Duration {
	if s.config == nil {
		return 10 * time.Minute // fallback if config is nil
	}
	if s.config.PendingSessionCookieMaxAge == 0 {
		return 10 * time.Minute // default if not configured
	}
	return time.Duration(s.config.PendingSessionCookieMaxAge)
}

// getSessionExpiration returns the session cookie expiration duration from config
func (s *PAMOIDCProvider) getSessionExpiration() time.Duration {
	if s.config == nil {
		return 30 * time.Minute // fallback if config is nil
	}
	if s.config.AuthenticatedSessionCookieMaxAge == 0 {
		return 30 * time.Minute // default if not configured
	}
	return time.Duration(s.config.AuthenticatedSessionCookieMaxAge)
}

// GetPendingAuthExpiration returns the pending auth cookie expiration duration from config
// This is a public method for use by handlers
func (s *PAMOIDCProvider) GetPendingAuthExpiration() time.Duration {
	return s.getPendingAuthExpiration()
}

// GetSessionExpiration returns the session cookie expiration duration from config
// This is a public method for use by handlers
func (s *PAMOIDCProvider) GetSessionExpiration() time.Duration {
	return s.getSessionExpiration()
}

// Token implements OIDCProvider interface - handles OAuth2 token requests
func (s *PAMOIDCProvider) Token(ctx context.Context, req *pamapi.TokenRequest) (*pamapi.TokenResponse, error) {
	// Handle different grant types
	switch req.GrantType {
	case pamapi.RefreshToken:
		return s.handleRefreshTokenGrant(ctx, req)
	case pamapi.AuthorizationCode:
		return s.handleAuthorizationCodeGrant(ctx, req)
	case pamapi.Password:
		return s.handlePasswordGrant(ctx, req)
	default:
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.UnsupportedGrantType,
			ErrorDescription: lo.ToPtr("Unsupported grant_type. Supported values: authorization_code, refresh_token, password"),
		}
	}
}

// handleRefreshTokenGrant handles the refresh_token grant type
func (s *PAMOIDCProvider) handleRefreshTokenGrant(ctx context.Context, req *pamapi.TokenRequest) (*pamapi.TokenResponse, error) {
	// Validate required fields for refresh token flow
	if req.RefreshToken == nil || *req.RefreshToken == "" {
		s.log.Warnf("handleRefreshTokenGrant: missing refresh token")
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidRequest,
			ErrorDescription: lo.ToPtr("Missing required parameter: refresh_token"),
		}
	}

	// Validate the refresh token and ensure it's actually a refresh token
	identity, err := s.jwtGenerator.ValidateTokenWithType(*req.RefreshToken, TokenTypeRefresh)
	if err != nil {
		s.log.Warnf("handleRefreshTokenGrant: failed to validate refresh token - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidGrant,
			ErrorDescription: lo.ToPtr("Invalid or expired refresh token"),
		}
	}

	// Get current user information from NSS to ensure user still exists
	systemUser, err := s.pamAuthenticator.LookupUser(identity.GetUsername())
	if err != nil {
		s.log.Errorf("handleRefreshTokenGrant: failed to lookup user - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidGrant,
			ErrorDescription: lo.ToPtr("User no longer exists or is disabled"),
		}
	}

	// Get current user groups for roles
	groups, err := s.pamAuthenticator.GetUserGroups(systemUser)
	if err != nil {
		s.log.Errorf("handleRefreshTokenGrant: failed to get user groups - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.ServerError,
			ErrorDescription: lo.ToPtr("Failed to retrieve user groups"),
		}
	}

	// Map groups to roles and extract organizations
	roles := s.mapGroupsToRoles(groups)
	s.log.Debugf("handleRefreshTokenGrant: mapped groups to roles - %v", roles)
	organizations := s.extractOrganizations(groups)
	s.log.Debugf("handleRefreshTokenGrant: extracted organizations - %v", organizations)

	// Extract scopes from the refresh token
	scopes := ""
	if scopesClaim, ok := identity.GetClaim("scopes"); ok {
		if scopesStr, ok := scopesClaim.(string); ok {
			scopes = scopesStr
		}
	}

	tokenGenerationRequest := authn.TokenGenerationRequest{
		Username:      identity.GetUsername(),
		UID:           identity.GetUID(),
		Organizations: organizations,
		Roles:         roles,
		Audience:      []string{s.config.ClientID}, // Set audience to client ID
		Issuer:        s.config.Issuer,             // Set issuer
		Scopes:        scopes,                      // Include scopes from refresh token
	}
	// Generate new access token with configurable expiry
	accessTokenExpiration := s.getAccessTokenExpiration()
	accessToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, accessTokenExpiration, TokenTypeAccess)
	if err != nil {
		s.log.Errorf("handleRefreshTokenGrant: server error when generating access token - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.ServerError,
			ErrorDescription: lo.ToPtr("Failed to generate access token"),
		}
	}

	// Create token response
	tokenResponse := &pamapi.TokenResponse{
		AccessToken: accessToken,
		TokenType:   pamapi.Bearer,
		ExpiresIn:   lo.ToPtr(int(accessTokenExpiration.Seconds())),
	}

	// Generate id_token if openid scope was originally requested
	if strings.Contains(scopes, ScopeOpenID) {
		tokenResponse.IdToken = lo.ToPtr(accessToken)
	}

	// Always issue a new refresh token when using refresh_token grant
	// (if we have a refresh token, it means offline_access was originally granted)
	refreshTokenExpiration := s.getRefreshTokenExpiration()
	refreshToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, refreshTokenExpiration, TokenTypeRefresh)
	if err != nil {
		s.log.Errorf("handleRefreshTokenGrant: server error when generating refresh token - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.ServerError,
			ErrorDescription: lo.ToPtr("Failed to generate refresh token"),
		}
	}
	tokenResponse.RefreshToken = lo.ToPtr(refreshToken)

	return tokenResponse, nil
}

// handleAuthorizationCodeGrant handles the authorization_code grant type
func (s *PAMOIDCProvider) handleAuthorizationCodeGrant(ctx context.Context, req *pamapi.TokenRequest) (*pamapi.TokenResponse, error) {
	// Validate required fields for authorization code flow
	if req.Code == nil || *req.Code == "" {
		s.log.Warnf("handleAuthorizationCodeGrant: missing authorization code")
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidRequest,
			ErrorDescription: lo.ToPtr("Missing required parameter: code"),
		}
	}

	// Validate client ID
	if req.ClientId == nil || *req.ClientId == "" {
		s.log.Warnf("handleAuthorizationCodeGrant: missing client ID")
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidClient,
			ErrorDescription: lo.ToPtr("Missing required parameter: client_id"),
		}
	}
	if s.config == nil || s.config.ClientID != *req.ClientId {
		s.log.Warnf("handleAuthorizationCodeGrant: invalid client ID - expected=%s, got=%s", s.config.ClientID, *req.ClientId)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidClient,
			ErrorDescription: lo.ToPtr("Invalid client_id"),
		}
	}

	// Validate client authentication based on whether a secret is configured
	// If clientSecret is configured (non-empty), this is a confidential client and we require authentication
	// If clientSecret is empty, this is a public client (CLI, SPA) and PKCE is required
	if s.config.ClientSecret != "" {
		// Confidential client - require client_secret_post authentication
		if req.ClientSecret == nil {
			s.log.Warnf("handleAuthorizationCodeGrant: missing client secret")
			return nil, &pamapi.OAuth2Error{
				Code:             pamapi.InvalidClient,
				ErrorDescription: lo.ToPtr("Client authentication failed: missing client_secret"),
			}
		}
		if *req.ClientSecret != s.config.ClientSecret {
			s.log.Warnf("handleAuthorizationCodeGrant: invalid client secret")
			return nil, &pamapi.OAuth2Error{
				Code:             pamapi.InvalidClient,
				ErrorDescription: lo.ToPtr("Client authentication failed: invalid client_secret"),
			}
		}
	}

	// Validate and retrieve authorization code
	codeData, exists := s.codeStore.GetCode(*req.Code)
	if !exists {
		s.log.Warnf("handleAuthorizationCodeGrant: authorization code not found")
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidGrant,
			ErrorDescription: lo.ToPtr("Authorization code is invalid, expired, or already used"),
		}
	}

	// Validate that the client ID matches the stored code
	if codeData.ClientID != *req.ClientId {
		s.log.Warnf("handleAuthorizationCodeGrant: invalid client ID - expected=%s, got=%s", codeData.ClientID, *req.ClientId)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidGrant,
			ErrorDescription: lo.ToPtr("Authorization code was not issued to this client"),
		}
	}

	// SECURITY: Validate redirect_uri matches the one from authorization request (RFC 6749 Section 4.1.3)
	// This prevents authorization code interception attacks
	if req.RedirectUri == nil || *req.RedirectUri == "" {
		s.log.Warnf("handleAuthorizationCodeGrant: missing redirect_uri")
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidRequest,
			ErrorDescription: lo.ToPtr("Missing required parameter: redirect_uri"),
		}
	}
	if codeData.RedirectURI != *req.RedirectUri {
		s.log.Warnf("handleAuthorizationCodeGrant: redirect_uri mismatch - expected=%s, got=%s", codeData.RedirectURI, *req.RedirectUri)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidGrant,
			ErrorDescription: lo.ToPtr(fmt.Sprintf("redirect_uri '%s' does not match the one used in the authorization request", *req.RedirectUri)),
		}
	}

	// SECURITY: Verify PKCE for public clients (required unless config allows) and confidential clients (if used)
	// Public clients MUST use PKCE per OAuth 2.0 Security BCP, unless explicitly allowed by config
	// SECURITY: Require PKCE for public clients (no client secret configured)
	// Per OAuth 2.0 Security BCP, public clients MUST use PKCE unless explicitly allowed by config

	codeVerifier := lo.FromPtrOr(req.CodeVerifier, "")
	codeChallenge := codeData.CodeChallenge
	if codeChallenge == "" && codeVerifier != "" {
		s.log.Warnf("handleAuthorizationCodeGrant: code_challenge required when code_verifier is provided")
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidGrant,
			ErrorDescription: lo.ToPtr("code_verifier provided but no code_challenge was used in authorization request"),
		}
	}
	if codeChallenge != "" && codeVerifier == "" {
		s.log.Warnf("handleAuthorizationCodeGrant: code_verifier required when code_challenge is provided")
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidGrant,
			ErrorDescription: lo.ToPtr("Missing required parameter: code_verifier (PKCE is required)"),
		}
	}
	if codeChallenge != "" {
		if !verifyPKCEChallenge(codeVerifier, codeChallenge, codeData.CodeChallengeMethod) {
			s.log.Warnf("handleAuthorizationCodeGrant: PKCE verification failed for public client")
			return nil, &pamapi.OAuth2Error{
				Code:             pamapi.InvalidGrant,
				ErrorDescription: lo.ToPtr("PKCE verification failed: code_verifier does not match code_challenge"),
			}
		}
	}
	// Get user information from NSS
	systemUser, err := s.pamAuthenticator.LookupUser(codeData.Username)
	if err != nil {
		s.log.Errorf("handleAuthorizationCodeGrant: failed to lookup user - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidGrant,
			ErrorDescription: lo.ToPtr("User no longer exists or is disabled"),
		}
	}

	// Get user groups for roles
	groups, err := s.pamAuthenticator.GetUserGroups(systemUser)
	if err != nil {
		s.log.Errorf("handleAuthorizationCodeGrant: failed to get user groups - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.ServerError,
			ErrorDescription: lo.ToPtr("Failed to retrieve user groups"),
		}
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
		Scopes:        codeData.Scope,              // Include scopes from authorization request
	}
	// Generate access token with configurable expiry
	accessTokenExpiration := s.getAccessTokenExpiration()
	accessToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, accessTokenExpiration, TokenTypeAccess)
	if err != nil {
		s.log.Errorf("handleAuthorizationCodeGrant: server error when generating access token - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.ServerError,
			ErrorDescription: lo.ToPtr("Failed to generate access token"),
		}
	}

	// Create token response
	tokenResponse := &pamapi.TokenResponse{
		AccessToken: accessToken,
		TokenType:   pamapi.Bearer,
		ExpiresIn:   lo.ToPtr(int(accessTokenExpiration.Seconds())),
	}

	// Generate id_token if openid scope was requested
	if strings.Contains(codeData.Scope, ScopeOpenID) {
		tokenResponse.IdToken = lo.ToPtr(accessToken)
	}

	// Only generate refresh token if offline_access was requested
	if strings.Contains(codeData.Scope, ScopeOfflineAccess) {
		refreshTokenExpiration := s.getRefreshTokenExpiration()
		refreshToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, refreshTokenExpiration, TokenTypeRefresh)
		if err != nil {
			s.log.Errorf("handleAuthorizationCodeGrant: server error when generating refresh token - %v", err)
			return nil, &pamapi.OAuth2Error{
				Code:             pamapi.ServerError,
				ErrorDescription: lo.ToPtr("Failed to generate refresh token"),
			}
		}
		tokenResponse.RefreshToken = lo.ToPtr(refreshToken)
	}

	return tokenResponse, nil
}

// handlePasswordGrant handles the password grant type (Resource Owner Password Credentials)
// WARNING: This grant type is deprecated in OAuth 2.1 and should only be used for trusted first-party clients.
// It does not support MFA and has limited brute force detection capabilities.
func (s *PAMOIDCProvider) handlePasswordGrant(ctx context.Context, req *pamapi.TokenRequest) (*pamapi.TokenResponse, error) {
	// Validate required fields for password grant
	if req.Username == nil || *req.Username == "" {
		s.log.Error("handlePasswordGrant: missing username")
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidRequest,
			ErrorDescription: lo.ToPtr("Missing required parameter: username"),
		}
	}

	if req.Password == nil || *req.Password == "" {
		s.log.Error("handlePasswordGrant: missing password")
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidRequest,
			ErrorDescription: lo.ToPtr("Missing required parameter: password"),
		}
	}

	// Validate client ID if provided
	if req.ClientId != nil && *req.ClientId != "" {
		if s.config == nil || s.config.ClientID != *req.ClientId {
			s.log.Errorf("handlePasswordGrant: invalid client ID - expected=%s, got=%s", s.config.ClientID, *req.ClientId)
			return nil, &pamapi.OAuth2Error{
				Code:             pamapi.InvalidClient,
				ErrorDescription: lo.ToPtr("Invalid client_id"),
			}
		}

		// Validate client authentication if client secret is configured
		if s.config.ClientSecret != "" {
			if req.ClientSecret == nil || *req.ClientSecret != s.config.ClientSecret {
				s.log.Error("handlePasswordGrant: invalid client secret")
				return nil, &pamapi.OAuth2Error{
					Code:             pamapi.InvalidClient,
					ErrorDescription: lo.ToPtr("Client authentication failed: invalid client_secret"),
				}
			}
		}
	}

	// Authenticate with PAM
	username := *req.Username
	if err := s.authenticateWithPAM(username, *req.Password); err != nil {
		s.log.Errorf("handlePasswordGrant: PAM authentication failed for user %s - %v", username, err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidGrant,
			ErrorDescription: lo.ToPtr("Invalid username or password"),
		}
	}
	s.log.Infof("handlePasswordGrant: PAM authentication successful for user %s", username)

	// Get user information from NSS
	systemUser, err := s.pamAuthenticator.LookupUser(username)
	if err != nil {
		s.log.Errorf("handlePasswordGrant: failed to lookup user - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.InvalidGrant,
			ErrorDescription: lo.ToPtr("User lookup failed"),
		}
	}

	// Get user groups for roles
	groups, err := s.pamAuthenticator.GetUserGroups(systemUser)
	if err != nil {
		s.log.Errorf("handlePasswordGrant: failed to get user groups - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.ServerError,
			ErrorDescription: lo.ToPtr("Failed to retrieve user groups"),
		}
	}

	// Map groups to roles and extract organizations
	roles := s.mapGroupsToRoles(groups)
	s.log.Debugf("handlePasswordGrant: mapped groups to roles - %v", roles)
	organizations := s.extractOrganizations(groups)
	s.log.Debugf("handlePasswordGrant: extracted organizations - %v", organizations)

	// Determine scopes - use requested scope or default
	scopes := lo.FromPtrOr(req.Scope, DefaultScopes)

	// Determine audience - use client ID if provided, otherwise use configured client ID
	audience := s.config.ClientID
	if req.ClientId != nil && *req.ClientId != "" {
		audience = *req.ClientId
	}

	tokenGenerationRequest := authn.TokenGenerationRequest{
		Username:      username,
		UID:           username,
		Organizations: organizations,
		Roles:         roles,
		Audience:      []string{audience},
		Issuer:        s.config.Issuer,
		Scopes:        scopes,
	}

	// Generate access token with configurable expiry
	accessTokenExpiration := s.getAccessTokenExpiration()
	accessToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, accessTokenExpiration, TokenTypeAccess)
	if err != nil {
		s.log.Errorf("handlePasswordGrant: server error when generating access token - %v", err)
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.ServerError,
			ErrorDescription: lo.ToPtr("Failed to generate access token"),
		}
	}

	// Create token response
	tokenResponse := &pamapi.TokenResponse{
		AccessToken: accessToken,
		TokenType:   pamapi.Bearer,
		ExpiresIn:   lo.ToPtr(int(accessTokenExpiration.Seconds())),
	}

	// Generate id_token if openid scope was requested
	if strings.Contains(scopes, ScopeOpenID) {
		tokenResponse.IdToken = lo.ToPtr(accessToken)
	}

	// Generate refresh token if offline_access scope was requested
	if strings.Contains(scopes, ScopeOfflineAccess) {
		refreshTokenExpiration := s.getRefreshTokenExpiration()
		refreshToken, err := s.jwtGenerator.GenerateTokenWithType(tokenGenerationRequest, refreshTokenExpiration, TokenTypeRefresh)
		if err != nil {
			s.log.Errorf("handlePasswordGrant: server error when generating refresh token - %v", err)
			return nil, &pamapi.OAuth2Error{
				Code:             pamapi.ServerError,
				ErrorDescription: lo.ToPtr("Failed to generate refresh token"),
			}
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
		return nil, errors.New("invalid_request")
	}

	// Validate client ID
	if s.config == nil {
		s.log.Errorf("Authorize: config is nil")
		return nil, errors.New("invalid_client")
	}
	if s.config.ClientID != req.ClientId {
		s.log.Warnf("Authorize: invalid client ID - expected=%s, got=%s", s.config.ClientID, req.ClientId)
		return nil, errors.New("invalid_client")
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
		return nil, errors.New("invalid_request")
	}

	// Validate response type
	if req.ResponseType != pamapi.Code {
		s.log.Warnf("Authorize: unsupported response type - %s", req.ResponseType)
		return nil, errors.New("unsupported_grant_type")
	}
	s.log.Debugf("Authorize: response type validation passed - %s", req.ResponseType)

	// SECURITY: Require PKCE for public clients (no client secret configured)
	// Per OAuth 2.0 Security BCP, public clients MUST use PKCE unless explicitly allowed by config
	isPublicClient := s.config.ClientSecret == ""
	codeChallenge := lo.FromPtrOr(req.CodeChallenge, "")
	codeChallengeMethod := lo.FromPtrOr(req.CodeChallengeMethod, "")
	if codeChallenge != "" && codeChallengeMethod == "" {
		s.log.Warnf("Authorize: code_challenge_method required when code_challenge is provided")
		return nil, errors.New("invalid_request")
	}
	if codeChallengeMethod != "" && codeChallenge == "" {
		s.log.Warnf("Authorize: code_challenge required when code_challenge_method is provided")
		return nil, errors.New("invalid_request")
	}
	if codeChallengeMethod != "" && codeChallengeMethod != pamapi.AuthAuthorizeParamsCodeChallengeMethodS256 {
		s.log.Warnf("Authorize: unsupported code_challenge_method - %s (only S256 supported)", codeChallengeMethod)
		return nil, errors.New("invalid_request")
	}
	if isPublicClient && codeChallenge == "" && !s.config.AllowPublicClientWithoutPKCE {
		s.log.Warnf("Authorize: PKCE required for public client but code_challenge not provided")
		return nil, errors.New("invalid_request")
	}

	// Authorization flow:
	// 1. Check if user is already authenticated (session/cookie)
	// 2. If not authenticated, return embedded login form
	// 3. User submits credentials via POST to /auth/login
	// 4. Server validates with PAM and generates authorization code
	// 5. Server redirects back to client with code

	// Extract encrypted session cookie from request context
	encryptedSessionCookie := s.extractSessionCookie(ctx, req)
	s.log.Debugf("Authorize: extracted session cookie")

	// Check if user is already authenticated via session
	if encryptedSessionCookie == "" {
		s.log.Debugf("Authorize: no session cookie found, creating encrypted cookie and returning login form")
		return s.createEncryptedCookieAndReturnLoginForm(req, codeChallenge, codeChallengeMethod)
	}

	// Decrypt and validate session cookie
	authData, exists := s.IsUserAuthenticated(encryptedSessionCookie)
	if !exists {
		s.log.Debugf("Authorize: session cookie invalid or expired, creating encrypted cookie and returning login form")
		return s.createEncryptedCookieAndReturnLoginForm(req, codeChallenge, codeChallengeMethod)
	}

	// Check if user is actually logged in (should always be true now, but check for safety)
	if !authData.IsLoggedIn {
		s.log.Debugf("Authorize: session exists but user not logged in, creating encrypted cookie and returning login form")
		return s.createEncryptedCookieAndReturnLoginForm(req, codeChallenge, codeChallengeMethod)
	}

	// Update session with any new parameters (in case they changed)
	state := lo.FromPtrOr(req.State, "")
	updatedScope := authData.Scope
	if req.Scope != nil && *req.Scope != "" {
		updatedScope = *req.Scope // Update scope if provided
	}

	// Update authData with new parameters for use in authorization code generation
	authData.State = state
	authData.CodeChallenge = codeChallenge
	authData.CodeChallengeMethod = string(codeChallengeMethod)
	authData.ClientID = req.ClientId
	authData.RedirectURI = req.RedirectUri
	authData.Scope = updatedScope

	// User is authenticated, get username from session
	username := authData.Username
	s.log.Infof("Authorize: user authenticated via session - username=%s", username)

	// Generate OAuth2 authorization code (step 4: used to exchange for access token)
	authCode, err := generateAuthorizationCode()
	if err != nil {
		s.log.Errorf("Authorize: failed to generate authorization code - %v", err)
		return nil, errors.New("server_error")
	}
	s.log.Debugf("Authorize: generated authorization code")

	// Use scope from session (preserves original request), otherwise determine based on user's role/group membership
	scopes := authData.Scope
	if scopes == "" {
		scopes = s.determineUserScopes(username)
	}
	s.log.Debugf("Authorize: determined scopes - %s", scopes)

	// Store authorization code with expiration (10 minutes)
	// Use values from session (which were validated above) to prevent parameter tampering
	codeData := &AuthorizationCodeData{
		Code:                authCode,
		ClientID:            authData.ClientID,
		RedirectURI:         authData.RedirectURI,
		Scope:               scopes,
		State:               authData.State,
		Username:            username,
		ExpiresAt:           time.Now().Add(10 * time.Minute),
		CreatedAt:           time.Now(),
		CodeChallenge:       authData.CodeChallenge,
		CodeChallengeMethod: pamapi.AuthAuthorizeParamsCodeChallengeMethod(authData.CodeChallengeMethod),
	}

	s.codeStore.StoreCode(codeData)
	s.log.Debugf("Authorize: stored authorization code for user - %s", username)

	// Build redirect URL with authorization code
	// Use authData.RedirectURI (already validated above) instead of req.RedirectUri
	parsed, err := url.Parse(authData.RedirectURI)
	if err != nil {
		s.log.Errorf("Authorize: failed to parse redirect URI - %v", err)
		return nil, fmt.Errorf("invalid redirect URI: %w", err)
	}

	values := parsed.Query()
	values.Set("code", authCode)
	// Use authData.State (already validated above) instead of req.State
	if authData.State != "" {
		values.Set("state", authData.State)
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
// encryptedCookie contains the encrypted authorization request parameters
func (s *PAMOIDCProvider) Login(ctx context.Context, username, password, encryptedCookie string) (*LoginResult, error) {
	s.log.Debugf("Login: attempting authentication for user %s", username)

	// Decrypt cookie to get authorization parameters
	pendingReq, err := s.DecryptCookieData(encryptedCookie)
	if err != nil {
		s.log.Warnf("Login: failed to decrypt cookie - %v", err)
		return nil, errors.New("invalid_request")
	}

	// Validate credentials with PAM/NSS
	s.log.Debugf("Login: calling PAM authentication for user %s", username)
	if err := s.authenticateWithPAM(username, password); err != nil {
		s.log.Errorf("Login: PAM authentication failed for user %s - %v", username, err)
		return nil, errors.New("invalid_grant")
	}
	s.log.Infof("Login: PAM authentication successful for user %s", username)

	// User is authenticated, create encrypted session cookie
	encryptedSessionCookie, err := s.CreateUserSession(username, pendingReq)
	if err != nil {
		s.log.Errorf("Login: failed to create encrypted session cookie for user %s - %v", username, err)
		return nil, errors.New("server_error")
	}
	s.log.Debugf("Login: created encrypted session cookie for %s", username)

	// Redirect back to authorization endpoint with all parameters
	authURL := fmt.Sprintf("/api/v1/auth/authorize?response_type=code&client_id=%s&redirect_uri=%s",
		url.QueryEscape(pendingReq.ClientID), url.QueryEscape(pendingReq.RedirectURI))
	if pendingReq.State != "" {
		authURL += fmt.Sprintf("&state=%s", url.QueryEscape(pendingReq.State))
	}
	if pendingReq.Scope != "" {
		authURL += fmt.Sprintf("&scope=%s", url.QueryEscape(pendingReq.Scope))
	}
	if pendingReq.CodeChallenge != "" {
		authURL += fmt.Sprintf("&code_challenge=%s", url.QueryEscape(pendingReq.CodeChallenge))
	}
	if pendingReq.CodeChallengeMethod != "" {
		authURL += fmt.Sprintf("&code_challenge_method=%s", url.QueryEscape(pendingReq.CodeChallengeMethod))
	}

	s.log.Debugf("Login: returning redirect for user %s", username)
	return &LoginResult{
		RedirectURL: authURL,
		SessionID:   encryptedSessionCookie, // Encrypted session cookie value
	}, nil
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
		return nil, &pamapi.OAuth2Error{
			Code:             "invalid_token",
			ErrorDescription: lo.ToPtr("Invalid or expired access token"),
		}
	}

	// Get user information from NSS
	systemUser, err := s.pamAuthenticator.LookupUser(identity.GetUsername())
	if err != nil {
		return nil, &pamapi.OAuth2Error{
			Code:             "invalid_token",
			ErrorDescription: lo.ToPtr("User no longer exists"),
		}
	}

	// Get user groups for roles
	groups, err := s.pamAuthenticator.GetUserGroups(systemUser)
	if err != nil {
		return nil, &pamapi.OAuth2Error{
			Code:             pamapi.ServerError,
			ErrorDescription: lo.ToPtr("Failed to retrieve user information"),
		}
	}

	// Map groups to roles and extract organizations
	roles := s.mapGroupsToRoles(groups)
	organizations := s.extractOrganizations(groups)

	// Create user info response
	userInfo := &pamapi.UserInfoResponse{
		Sub:               identity.GetUsername(), // Required field
		PreferredUsername: lo.ToPtr(identity.GetUsername()),
		Name:              lo.ToPtr(systemUser.Name),
		Email:             lo.ToPtr(""), // Email not available from system user
		EmailVerified:     lo.ToPtr(false),
		Roles:             &roles,
		Organizations:     &organizations,
	}

	return userInfo, nil // Success - no OAuth2Error
}

// GetOpenIDConfiguration returns the OpenID Connect configuration
func (s *PAMOIDCProvider) GetOpenIDConfiguration() (*pamapi.OpenIDConfiguration, error) {
	// Use issuer from config
	if s.config == nil || s.config.Issuer == "" {
		return nil, fmt.Errorf("issuer URL not configured")
	}
	issuer := s.config.Issuer

	// Response types and grant types are determined by implementation
	responseTypes := []string{"code"}                                         // Support authorization code flow
	grantTypes := []string{"authorization_code", "refresh_token", "password"} // Support OAuth2 flows (including deprecated password grant)

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

	// Advertise PKCE support as per RFC 7636
	// Only S256 code challenge method is supported (plain is not secure)
	codeChallengeMethodsSupported := []pamapi.OpenIDConfigurationCodeChallengeMethodsSupported{
		pamapi.OpenIDConfigurationCodeChallengeMethodsSupportedS256,
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
		CodeChallengeMethodsSupported:     &codeChallengeMethodsSupported,
	}, nil
}

// GetJWKS returns the JSON Web Key Set
func (s *PAMOIDCProvider) GetJWKS() (*pamapi.JWKSResponse, error) {
	// Use the JWT generator's GetJWKS method
	return s.jwtGenerator.GetJWKS()
}

// mapGroupsToRoles maps system groups to flightctl roles
// Groups starting with "org-" are treated as organizations, not roles
// Groups containing a dot (e.g., "myorg.role1") have the first dot replaced with colon (myorg:role1)
//
// All other groups are returned as-is and become roles
func (s *PAMOIDCProvider) mapGroupsToRoles(groups []string) []string {
	var roles []string
	roleSet := make(map[string]struct{}) // Use set to avoid duplicates

	for _, group := range groups {
		// Skip organization groups (they start with "org-")
		if strings.HasPrefix(group, OrgPrefix) {
			continue
		}

		// Check if group contains a dot (e.g., "myorg.role1")
		// Replace first dot with colon: myorg.role1 -> myorg:role1
		if strings.Contains(group, ".") {
			role := strings.Replace(group, ".", ":", 1)
			if _, exists := roleSet[role]; !exists {
				roles = append(roles, role)
				roleSet[role] = struct{}{}
			}
			continue
		}

		// Keep groups as-is (they become roles)
		if _, exists := roleSet[group]; !exists {
			roles = append(roles, group)
			roleSet[group] = struct{}{}
		}
	}

	return roles
}

// extractOrganizations extracts organization names from groups
// Groups starting with "org-" are treated as organizations
func (s *PAMOIDCProvider) extractOrganizations(groups []string) []string {
	var organizations []string
	orgSet := make(map[string]struct{}) // Use set to avoid duplicates

	for _, group := range groups {
		if strings.HasPrefix(group, OrgPrefix) {
			// Extract organization name (remove "org-" prefix)
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

// IsUserAuthenticated checks if a user is authenticated via encrypted session cookie
// Returns the decrypted auth data if valid, or nil if invalid/expired
func (s *PAMOIDCProvider) IsUserAuthenticated(encryptedCookie string) (*EncryptedAuthData, bool) {
	if encryptedCookie == "" {
		return nil, false
	}

	encryptedData, err := s.DecryptSessionData(encryptedCookie)
	if err != nil {
		s.log.Debugf("IsUserAuthenticated: failed to decrypt session cookie - %v", err)
		return nil, false
	}

	return encryptedData, true
}

// CreateUserSession creates a new encrypted session cookie from pending auth data
// Returns the encrypted cookie value to be set in the client's browser
func (s *PAMOIDCProvider) CreateUserSession(username string, pendingReq *EncryptedAuthData) (string, error) {
	now := time.Now()
	sessionExpiration := s.getSessionExpiration()
	encryptedData := &EncryptedAuthData{
		ClientID:            pendingReq.ClientID,
		RedirectURI:         pendingReq.RedirectURI,
		Scope:               pendingReq.Scope,
		State:               pendingReq.State,
		CodeChallenge:       pendingReq.CodeChallenge,
		CodeChallengeMethod: pendingReq.CodeChallengeMethod,
		ExpiresAt:           now.Add(sessionExpiration).Unix(),
		Username:            username,
		IsLoggedIn:          true,
		LoginTime:           now.Unix(),
	}

	encryptedCookie, err := s.EncryptSessionData(encryptedData)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt session data: %w", err)
	}

	return encryptedCookie, nil
}

// extractSessionCookie extracts encrypted session cookie from request context
func (s *PAMOIDCProvider) extractSessionCookie(ctx context.Context, req *pamapi.AuthAuthorizeParams) string {
	// Check for session cookie in context (standard OAuth2/OIDC session management)
	if sessionCookie, ok := ctx.Value(SessionCookieCtxKey).(string); ok && sessionCookie != "" {
		return sessionCookie
	}

	// No session found
	return ""
}

// GetLoginFormTemplate returns the login form template for safe execution
// The template uses html/template which automatically escapes all user input
func (s *PAMOIDCProvider) GetLoginFormTemplate() *template.Template {
	return s.loginFormTemplate
}

// createEncryptedCookieAndReturnLoginForm creates an encrypted cookie with authorization parameters
// and returns a login form response. Used when user is not authenticated or session is invalid.
func (s *PAMOIDCProvider) createEncryptedCookieAndReturnLoginForm(req *pamapi.AuthAuthorizeParams, codeChallenge string, codeChallengeMethod pamapi.AuthAuthorizeParamsCodeChallengeMethod) (*AuthorizeResponse, error) {
	// Create pending auth request data (IsLoggedIn is false, Username is empty)
	pendingAuthExpiration := s.getPendingAuthExpiration()
	pendingReq := &EncryptedAuthData{
		ClientID:            req.ClientId,
		RedirectURI:         req.RedirectUri,
		Scope:               lo.FromPtrOr(req.Scope, ""),
		State:               lo.FromPtrOr(req.State, ""),
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: string(codeChallengeMethod),
		ExpiresAt:           time.Now().Add(pendingAuthExpiration).Unix(),
		Username:            "", // Empty for pending auth
		IsLoggedIn:          false,
		LoginTime:           0, // Not set for pending auth
	}

	// Encrypt and encode as cookie value
	encryptedCookie, err := s.EncryptCookieData(pendingReq)
	if err != nil {
		s.log.Errorf("Authorize: failed to encrypt cookie data - %v", err)
		return nil, errors.New("server_error")
	}

	// Return login form - cookie will be set by handler
	loginForm := s.GetLoginForm()
	return &AuthorizeResponse{
		Type:      AuthorizeResponseTypeHTML,
		Content:   loginForm,
		SessionID: encryptedCookie, // Reuse SessionID field to pass encrypted cookie value
	}, nil
}

// GetLoginForm returns the HTML for the login form
// Uses html/template to safely escape user input and prevent XSS attacks
// All authorization parameters are stored in encrypted cookie, form doesn't need them
func (s *PAMOIDCProvider) GetLoginForm() string {
	var buf bytes.Buffer
	if err := s.loginFormTemplate.Execute(&buf, LoginFormData{}); err != nil {
		return loginFormErrorHTML
	}

	return buf.String()
}
