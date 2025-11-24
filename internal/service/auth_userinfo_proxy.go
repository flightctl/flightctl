package service

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
)

// AuthUserInfoProxy handles OIDC UserInfo requests by extracting identity from context
type AuthUserInfoProxy struct {
	authN common.AuthNMiddleware
}

// NewAuthUserInfoProxy creates a new auth userinfo proxy
func NewAuthUserInfoProxy(authN common.AuthNMiddleware) *AuthUserInfoProxy {
	return &AuthUserInfoProxy{
		authN: authN,
	}
}

// ProxyUserInfoRequest handles OIDC UserInfo requests by extracting the authenticated identity from context
func (p *AuthUserInfoProxy) ProxyUserInfoRequest(ctx context.Context) (*api.UserInfoResponse, api.Status) {
	// Extract mapped identity from context (set by auth middleware)
	mappedIdentity, ok := ctx.Value(consts.MappedIdentityCtxKey).(*identity.MappedIdentity)
	if !ok || mappedIdentity == nil {
		return createErrorUserInfoResponse("invalid_token"), api.StatusUnauthorized("No authenticated identity found")
	}

	// Convert mapped identity to UserInfoResponse
	return mappedIdentityToUserInfoResponse(mappedIdentity), api.StatusOK()
}

// mappedIdentityToUserInfoResponse converts a MappedIdentity to an api.UserInfoResponse
func mappedIdentityToUserInfoResponse(mappedIdentity *identity.MappedIdentity) *api.UserInfoResponse {
	response := &api.UserInfoResponse{}

	// Set subject (UID)
	if mappedIdentity.UID != "" {
		response.Sub = &mappedIdentity.UID
	}

	// Set preferred username
	if mappedIdentity.Username != "" {
		response.PreferredUsername = &mappedIdentity.Username
		// Also use as name if nothing else is available
		if response.Name == nil {
			response.Name = &mappedIdentity.Username
		}
	}

	// Set organizations (full objects, not just names)
	if len(mappedIdentity.Organizations) > 0 {
		orgs := make([]api.Organization, 0, len(mappedIdentity.Organizations))
		for _, org := range mappedIdentity.Organizations {
			orgs = append(orgs, organizationModelToAPI(org))
		}
		response.Organizations = &orgs
	}

	return response
}

// createErrorUserInfoResponse creates an error UserInfo response
func createErrorUserInfoResponse(errorCode string) *api.UserInfoResponse {
	return &api.UserInfoResponse{
		Error: &errorCode,
	}
}
