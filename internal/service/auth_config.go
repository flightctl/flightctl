package service

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
)

// GetAuthConfig returns the authentication configuration
// The auth config from the middleware already includes all static and dynamic providers
func (h *ServiceHandler) GetAuthConfig(ctx context.Context, authConfig *domain.AuthConfig) (*domain.AuthConfig, domain.Status) {
	return authConfig, domain.StatusOK()
}
