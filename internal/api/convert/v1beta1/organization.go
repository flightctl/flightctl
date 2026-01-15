package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// OrganizationConverter converts between v1beta1 API types and domain types for Organization resources.
type OrganizationConverter interface {
	ListFromDomain(*domain.OrganizationList) *apiv1beta1.OrganizationList

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListOrganizationsParams) domain.ListOrganizationsParams
}

type organizationConverter struct{}

// NewOrganizationConverter creates a new OrganizationConverter.
func NewOrganizationConverter() OrganizationConverter {
	return &organizationConverter{}
}

func (c *organizationConverter) ListFromDomain(l *domain.OrganizationList) *apiv1beta1.OrganizationList {
	return l
}

func (c *organizationConverter) ListParamsToDomain(p apiv1beta1.ListOrganizationsParams) domain.ListOrganizationsParams {
	return p
}
