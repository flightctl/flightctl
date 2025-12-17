package pam

import (
	"context"

	pamapi "github.com/flightctl/flightctl/api/v1beta1/pam-issuer"
)

// AuthorizeResponseType indicates the type of response from the authorize endpoint
type AuthorizeResponseType string

const (
	AuthorizeResponseTypeHTML     AuthorizeResponseType = "html"     // HTML login form
	AuthorizeResponseTypeRedirect AuthorizeResponseType = "redirect" // Redirect URL
)

// AuthorizeResponse wraps the authorize endpoint response with metadata
type AuthorizeResponse struct {
	Type      AuthorizeResponseType
	Content   string
	SessionID string // Session ID to set as cookie (for pending sessions)
}

// LoginResult contains the result of a successful login
type LoginResult struct {
	RedirectURL string
	SessionID   string
}

// OIDCIssuer defines the interface for OIDC token issuers
// This handles token issuance only - validation is handled by existing auth modules
type OIDCIssuer interface {
	// Token Issuance (OAuth2/OIDC flows)
	// Returns TokenResponse on success, or OAuth2Error (implements error interface) on failure
	Token(ctx context.Context, req *pamapi.TokenRequest) (*pamapi.TokenResponse, error)

	// UserInfo (OIDC endpoint)
	// Returns UserInfoResponse on success, or OAuth2Error (implements error interface) on failure
	UserInfo(ctx context.Context, accessToken string) (*pamapi.UserInfoResponse, error)

	// Authorization Code Flow (browser-based, uses redirects/HTML for errors)
	Authorize(ctx context.Context, req *pamapi.AuthAuthorizeParams) (*AuthorizeResponse, error)

	// Login handles the login form submission (browser-based)
	// encryptedCookie contains the encrypted authorization request parameters
	Login(ctx context.Context, username, password, encryptedCookie string) (*LoginResult, error)

	// Discovery and Configuration (system errors only)
	GetOpenIDConfiguration() (*pamapi.OpenIDConfiguration, error)
	GetJWKS() (*pamapi.JWKSResponse, error)
}
