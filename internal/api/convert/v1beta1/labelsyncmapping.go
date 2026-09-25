package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

type LabelSyncMappingConverter interface {
	ToDomain(apiv1beta1.LabelSyncMapping) domain.LabelSyncMapping
	FromDomain(*domain.LabelSyncMapping) *apiv1beta1.LabelSyncMapping
	ListFromDomain(*domain.LabelSyncMappingList) *apiv1beta1.LabelSyncMappingList
	ListParamsToDomain(apiv1beta1.ListLabelSyncMappingsParams) domain.ListLabelSyncMappingsParams
}

type labelSyncMappingConverter struct{}

func NewLabelSyncMappingConverter() LabelSyncMappingConverter { return &labelSyncMappingConverter{} }
func (*labelSyncMappingConverter) ToDomain(mapping apiv1beta1.LabelSyncMapping) domain.LabelSyncMapping {
	return mapping
}
func (*labelSyncMappingConverter) FromDomain(mapping *domain.LabelSyncMapping) *apiv1beta1.LabelSyncMapping {
	return mapping
}
func (*labelSyncMappingConverter) ListFromDomain(list *domain.LabelSyncMappingList) *apiv1beta1.LabelSyncMappingList {
	return list
}
func (*labelSyncMappingConverter) ListParamsToDomain(params apiv1beta1.ListLabelSyncMappingsParams) domain.ListLabelSyncMappingsParams {
	return params
}
