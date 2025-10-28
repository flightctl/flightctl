package util

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/issuer"
)

// MockOIDCIssuer is a mock implementation of issuer.OIDCIssuer for testing
type MockOIDCIssuer struct{}

// NewMockOIDCIssuer creates a new mock OIDC issuer
func NewMockOIDCIssuer() issuer.OIDCIssuer {
	return &MockOIDCIssuer{}
}

// Token implements the OIDCIssuer interface
func (m *MockOIDCIssuer) Token(ctx context.Context, req *v1alpha1.TokenRequest) (*v1alpha1.TokenResponse, error) {
	return &v1alpha1.TokenResponse{
		Error: stringPtr("unsupported_grant_type"),
	}, nil
}

// UserInfo implements the OIDCIssuer interface
func (m *MockOIDCIssuer) UserInfo(ctx context.Context, accessToken string) (*v1alpha1.UserInfoResponse, error) {
	return &v1alpha1.UserInfoResponse{
		Error: stringPtr("invalid_token"),
	}, nil
}

// GetOpenIDConfiguration implements the OIDCIssuer interface
func (m *MockOIDCIssuer) GetOpenIDConfiguration(baseURL string) (*v1alpha1.OpenIDConfiguration, error) {
	return &v1alpha1.OpenIDConfiguration{
		Issuer: &baseURL,
	}, nil
}

// GetJWKS implements the OIDCIssuer interface
func (m *MockOIDCIssuer) GetJWKS() (*v1alpha1.JWKSResponse, error) {
	emptyKeys := []struct {
		Alg *string `json:"alg,omitempty"`
		E   *string `json:"e,omitempty"`
		Kid *string `json:"kid,omitempty"`
		Kty *string `json:"kty,omitempty"`
		N   *string `json:"n,omitempty"`
		Use *string `json:"use,omitempty"`
	}{}
	return &v1alpha1.JWKSResponse{
		Keys: &emptyKeys,
	}, nil
}

// Authorize implements the OIDCIssuer interface
func (m *MockOIDCIssuer) Authorize(ctx context.Context, req *v1alpha1.AuthAuthorizeParams) (*issuer.AuthorizeResponse, error) {
	return &issuer.AuthorizeResponse{
		Type:    issuer.AuthorizeResponseTypeRedirect,
		Content: "invalid_request",
	}, nil
}

// Login implements the OIDCIssuer interface
func (m *MockOIDCIssuer) Login(ctx context.Context, username, password, clientID, redirectURI, state string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}
