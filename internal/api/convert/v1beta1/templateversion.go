package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// TemplateVersionConverter converts between v1beta1 API types and domain types for TemplateVersion resources.
type TemplateVersionConverter interface {
	FromDomain(*domain.TemplateVersion) *apiv1beta1.TemplateVersion
	ListFromDomain(*domain.TemplateVersionList) *apiv1beta1.TemplateVersionList

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListTemplateVersionsParams) domain.ListTemplateVersionsParams
}

type templateVersionConverter struct{}

// NewTemplateVersionConverter creates a new TemplateVersionConverter.
func NewTemplateVersionConverter() TemplateVersionConverter {
	return &templateVersionConverter{}
}

func (c *templateVersionConverter) FromDomain(tv *domain.TemplateVersion) *apiv1beta1.TemplateVersion {
	return tv
}

func (c *templateVersionConverter) ListFromDomain(l *domain.TemplateVersionList) *apiv1beta1.TemplateVersionList {
	return l
}

func (c *templateVersionConverter) ListParamsToDomain(p apiv1beta1.ListTemplateVersionsParams) domain.ListTemplateVersionsParams {
	return p
}
