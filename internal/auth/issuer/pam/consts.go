//go:build linux

package pam

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

// SessionCookieCtxKey is the context key for storing session cookies
const SessionCookieCtxKey contextKey = "session_cookie"

// OAuth2 Scopes
const (
	// ScopeOfflineAccess is the OAuth2 scope for requesting refresh tokens
	ScopeOfflineAccess = "offline_access"
	// ScopeOpenID is the OpenID Connect scope
	ScopeOpenID = "openid"
	// ScopeProfile is the scope for accessing user profile information
	ScopeProfile = "profile"
	// ScopeEmail is the scope for accessing user email
	ScopeEmail = "email"
	// ScopeRoles is the scope for accessing user roles
	ScopeRoles = "roles"
	// DefaultScopes is the default set of scopes for authenticated users
	DefaultScopes = "openid profile email"
)

// Token Type Identifiers (used in JWT claims, not grant types)
const (
	// TokenTypeAccess identifies an access token in JWT claims
	TokenTypeAccess = "access_token"
	// TokenTypeRefresh identifies a refresh token in JWT claims
	TokenTypeRefresh = "refresh_token"
)

// OAuth2 Error Codes
const (
	// ErrorInvalidRequest indicates the request is missing a required parameter
	ErrorInvalidRequest = "invalid_request"
	// ErrorInvalidClient indicates client authentication failed
	ErrorInvalidClient = "invalid_client"
	// ErrorInvalidGrant indicates the authorization code or refresh token is invalid
	ErrorInvalidGrant = "invalid_grant"
	// ErrorUnsupportedGrantType indicates the grant type is not supported
	ErrorUnsupportedGrantType = "unsupported_grant_type"
	// ErrorServerError indicates an internal server error occurred
	ErrorServerError = "server_error"
	// ErrorInvalidToken indicates the access token is invalid or expired
	ErrorInvalidToken = "invalid_token"
)

// Organization and Group Prefixes
const (
	// OrgPrefix is the prefix for organization group names
	OrgPrefix = "org:"
)

// Token Endpoint Authentication Methods
const (
	// AuthMethodNone indicates no client authentication (public client)
	AuthMethodNone = "none"
	// AuthMethodClientSecretPost indicates client_secret_post authentication
	AuthMethodClientSecretPost = "client_secret_post"
)

// Default Signing Algorithms
const (
	// SigningAlgRS256 is the RS256 signing algorithm
	SigningAlgRS256 = "RS256"
)
