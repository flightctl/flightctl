package pam

import (
	"context"

	pamapi "github.com/flightctl/flightctl/api/v1alpha1/pam-issuer"
)

// AuthorizeResponseType indicates the type of response from the authorize endpoint
type AuthorizeResponseType string

const (
	AuthorizeResponseTypeHTML     AuthorizeResponseType = "html"     // HTML login form
	AuthorizeResponseTypeRedirect AuthorizeResponseType = "redirect" // Redirect URL
)

// AuthorizeResponse wraps the authorize endpoint response with metadata
type AuthorizeResponse struct {
	Type    AuthorizeResponseType
	Content string
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
	Token(ctx context.Context, req *pamapi.TokenRequest) (*pamapi.TokenResponse, error)
	UserInfo(ctx context.Context, accessToken string) (*pamapi.UserInfoResponse, error)

	// Authorization Code Flow
	Authorize(ctx context.Context, req *pamapi.AuthAuthorizeParams) (*AuthorizeResponse, error)

	// Login handles the login form submission
	Login(ctx context.Context, username, password, clientID, redirectURI, state string) (*LoginResult, error)

	// Discovery and Configuration
	GetOpenIDConfiguration() (*pamapi.OpenIDConfiguration, error)
	GetJWKS() (*pamapi.JWKSResponse, error)
}
