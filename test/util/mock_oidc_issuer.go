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
func (m *MockOIDCIssuer) GetOpenIDConfiguration(baseURL string) map[string]interface{} {
	return map[string]interface{}{
		"issuer": baseURL,
	}
}

// GetJWKS implements the OIDCIssuer interface
func (m *MockOIDCIssuer) GetJWKS() (map[string]interface{}, error) {
	return map[string]interface{}{
		"keys": []interface{}{},
	}, nil
}

// Authorize implements the OIDCIssuer interface
func (m *MockOIDCIssuer) Authorize(ctx context.Context, req *v1alpha1.AuthAuthorizeParams) (string, error) {
	return "invalid_request", nil
}

// Login implements the OIDCIssuer interface
func (m *MockOIDCIssuer) Login(ctx context.Context, username, password, clientID, redirectURI, state string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}
