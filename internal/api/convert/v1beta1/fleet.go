package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// FleetConverter converts between v1beta1 API types and domain types for Fleet resources.
type FleetConverter interface {
	ToDomain(apiv1beta1.Fleet) domain.Fleet
	FromDomain(*domain.Fleet) *apiv1beta1.Fleet
	ListFromDomain(*domain.FleetList) *apiv1beta1.FleetList

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListFleetsParams) domain.ListFleetsParams
	GetParamsToDomain(apiv1beta1.GetFleetParams) domain.GetFleetParams
}

type fleetConverter struct{}

// NewFleetConverter creates a new FleetConverter.
func NewFleetConverter() FleetConverter {
	return &fleetConverter{}
}

func (c *fleetConverter) ToDomain(f apiv1beta1.Fleet) domain.Fleet {
	return f
}

func (c *fleetConverter) FromDomain(f *domain.Fleet) *apiv1beta1.Fleet {
	return f
}

func (c *fleetConverter) ListFromDomain(l *domain.FleetList) *apiv1beta1.FleetList {
	return l
}

func (c *fleetConverter) ListParamsToDomain(p apiv1beta1.ListFleetsParams) domain.ListFleetsParams {
	return p
}

func (c *fleetConverter) GetParamsToDomain(p apiv1beta1.GetFleetParams) domain.GetFleetParams {
	return p
}
