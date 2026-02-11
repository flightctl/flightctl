package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// ResourceSyncConverter converts between v1beta1 API types and domain types for ResourceSync resources.
type ResourceSyncConverter interface {
	ToDomain(apiv1beta1.ResourceSync) domain.ResourceSync
	FromDomain(*domain.ResourceSync) *apiv1beta1.ResourceSync
	ListFromDomain(*domain.ResourceSyncList) *apiv1beta1.ResourceSyncList

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListResourceSyncsParams) domain.ListResourceSyncsParams
}

type resourceSyncConverter struct{}

// NewResourceSyncConverter creates a new ResourceSyncConverter.
func NewResourceSyncConverter() ResourceSyncConverter {
	return &resourceSyncConverter{}
}

func (c *resourceSyncConverter) ToDomain(rs apiv1beta1.ResourceSync) domain.ResourceSync {
	return rs
}

func (c *resourceSyncConverter) FromDomain(rs *domain.ResourceSync) *apiv1beta1.ResourceSync {
	return rs
}

func (c *resourceSyncConverter) ListFromDomain(l *domain.ResourceSyncList) *apiv1beta1.ResourceSyncList {
	return l
}

func (c *resourceSyncConverter) ListParamsToDomain(p apiv1beta1.ListResourceSyncsParams) domain.ListResourceSyncsParams {
	return p
}
