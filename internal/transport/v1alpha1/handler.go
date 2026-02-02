package transportv1alpha1

import (
	convertv1alpha1 "github.com/flightctl/flightctl/internal/api/convert/v1alpha1"
	serverv1alpha1 "github.com/flightctl/flightctl/internal/api/server/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
)

type TransportHandler struct {
	serviceHandler service.Service
	converter      convertv1alpha1.Converter
}

// Make sure we conform to servers Transport interface
var _ serverv1alpha1.Transport = (*TransportHandler)(nil)

func NewTransportHandler(serviceHandler service.Service, converter convertv1alpha1.Converter) *TransportHandler {
	return &TransportHandler{
		serviceHandler: serviceHandler,
		converter:      converter,
	}
}
