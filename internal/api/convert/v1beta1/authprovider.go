package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// AuthProviderConverter converts between v1beta1 API types and domain types for AuthProvider resources.
type AuthProviderConverter interface {
	ToDomain(apiv1beta1.AuthProvider) domain.AuthProvider
	FromDomain(*domain.AuthProvider) *apiv1beta1.AuthProvider
	ListFromDomain(*domain.AuthProviderList) *apiv1beta1.AuthProviderList

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListAuthProvidersParams) domain.ListAuthProvidersParams
}

type authProviderConverter struct{}

// NewAuthProviderConverter creates a new AuthProviderConverter.
func NewAuthProviderConverter() AuthProviderConverter {
	return &authProviderConverter{}
}

func (c *authProviderConverter) ToDomain(ap apiv1beta1.AuthProvider) domain.AuthProvider {
	return ap
}

func (c *authProviderConverter) FromDomain(ap *domain.AuthProvider) *apiv1beta1.AuthProvider {
	return ap
}

func (c *authProviderConverter) ListFromDomain(l *domain.AuthProviderList) *apiv1beta1.AuthProviderList {
	return l
}

func (c *authProviderConverter) ListParamsToDomain(p apiv1beta1.ListAuthProvidersParams) domain.ListAuthProvidersParams {
	return p
}
