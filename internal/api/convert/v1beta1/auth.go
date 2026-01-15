package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// AuthConverter converts between v1beta1 API types and domain types for authentication types.
type AuthConverter interface {
	TokenRequestToDomain(*apiv1beta1.TokenRequest) *domain.TokenRequest
	TokenResponseFromDomain(*domain.TokenResponse) *apiv1beta1.TokenResponse
}

type authConverter struct{}

// NewAuthConverter creates a new AuthConverter.
func NewAuthConverter() AuthConverter {
	return &authConverter{}
}

func (c *authConverter) TokenRequestToDomain(r *apiv1beta1.TokenRequest) *domain.TokenRequest {
	return r
}

func (c *authConverter) TokenResponseFromDomain(r *domain.TokenResponse) *apiv1beta1.TokenResponse {
	return r
}
