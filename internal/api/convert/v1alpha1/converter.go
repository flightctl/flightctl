package v1alpha1

import (
	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	apiversioning "github.com/flightctl/flightctl/api/versioning"
	"github.com/flightctl/flightctl/internal/domain"
)

// Converter aggregates all resource-specific converters for v1alpha1 API.
type Converter interface {
	Catalog() CatalogConverter
	Common() CommonConverter
}

type converterImpl struct {
	catalog CatalogConverter
	common  CommonConverter
}

// NewConverter creates a new Converter instance with all resource converters.
func NewConverter() Converter {
	return &converterImpl{
		catalog: NewCatalogConverter(),
		common:  NewCommonConverter(),
	}
}

func (c *converterImpl) Catalog() CatalogConverter {
	return c.catalog
}

func (c *converterImpl) Common() CommonConverter {
	return c.common
}

// CommonConverter converts common types shared across API versions.
type CommonConverter interface {
	PatchRequestToDomain(apiv1beta1.PatchRequest) domain.PatchRequest
	StatusFromDomain(domain.Status) apiv1alpha1.Status
}

type commonConverter struct{}

// NewCommonConverter creates a new CommonConverter.
func NewCommonConverter() CommonConverter {
	return &commonConverter{}
}

func (c *commonConverter) PatchRequestToDomain(p apiv1beta1.PatchRequest) domain.PatchRequest {
	return p
}

func (c *commonConverter) StatusFromDomain(s domain.Status) apiv1alpha1.Status {
	return apiv1alpha1.Status{
		ApiVersion: apiversioning.QualifiedV1Alpha1,
		Kind:       apiv1alpha1.StatusKind,
		Code:       s.Code,
		Message:    s.Message,
		Reason:     s.Reason,
		Status:     s.Status,
	}
}
