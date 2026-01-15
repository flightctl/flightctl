package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// EventConverter converts between v1beta1 API types and domain types for Event resources.
type EventConverter interface {
	ListFromDomain(*domain.EventList) *apiv1beta1.EventList

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListEventsParams) domain.ListEventsParams
}

type eventConverter struct{}

// NewEventConverter creates a new EventConverter.
func NewEventConverter() EventConverter {
	return &eventConverter{}
}

func (c *eventConverter) ListFromDomain(l *domain.EventList) *apiv1beta1.EventList {
	return l
}

func (c *eventConverter) ListParamsToDomain(p apiv1beta1.ListEventsParams) domain.ListEventsParams {
	return p
}
