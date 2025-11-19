package service

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// GetAuthConfig returns the authentication configuration
// The auth config from the middleware already includes all static and dynamic providers
func (h *ServiceHandler) GetAuthConfig(ctx context.Context, authConfig *api.AuthConfig) (*api.AuthConfig, api.Status) {
	return authConfig, api.StatusOK()
}
