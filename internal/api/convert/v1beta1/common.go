package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// CommonConverter converts between v1beta1 API types and domain types for common types.
type CommonConverter interface {
	PatchRequestToDomain(apiv1beta1.PatchRequest) domain.PatchRequest
	StatusFromDomain(domain.Status) apiv1beta1.Status
	LabelListFromDomain(*domain.LabelList) *apiv1beta1.LabelList

	// Params conversions
	ListLabelsParamsToDomain(apiv1beta1.ListLabelsParams) domain.ListLabelsParams
}

type commonConverter struct{}

// NewCommonConverter creates a new CommonConverter.
func NewCommonConverter() CommonConverter {
	return &commonConverter{}
}

func (c *commonConverter) PatchRequestToDomain(p apiv1beta1.PatchRequest) domain.PatchRequest {
	return p
}

func (c *commonConverter) StatusFromDomain(s domain.Status) apiv1beta1.Status {
	return s
}

func (c *commonConverter) LabelListFromDomain(l *domain.LabelList) *apiv1beta1.LabelList {
	return l
}

func (c *commonConverter) ListLabelsParamsToDomain(p apiv1beta1.ListLabelsParams) domain.ListLabelsParams {
	return p
}
