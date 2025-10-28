package issuer

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
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

// OIDCIssuer defines the interface for OIDC token issuers
// This handles token issuance only - validation is handled by existing auth modules
type OIDCIssuer interface {
	// Token Issuance (OAuth2/OIDC flows)
	Token(ctx context.Context, req *v1alpha1.TokenRequest) (*v1alpha1.TokenResponse, error)
	UserInfo(ctx context.Context, accessToken string) (*v1alpha1.UserInfoResponse, error)

	// Authorization Code Flow
	Authorize(ctx context.Context, req *v1alpha1.AuthAuthorizeParams) (*AuthorizeResponse, error)

	// Login handles the login form submission
	Login(ctx context.Context, username, password, clientID, redirectURI, state string) (string, error)

	// Discovery and Configuration
	GetOpenIDConfiguration(baseURL string) (*v1alpha1.OpenIDConfiguration, error)
	GetJWKS() (*v1alpha1.JWKSResponse, error)
}
