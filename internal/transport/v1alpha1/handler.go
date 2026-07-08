package transportv1alpha1

import (
	convertv1alpha1 "github.com/flightctl/flightctl/internal/api/convert/v1alpha1"
	serverv1alpha1 "github.com/flightctl/flightctl/internal/api/server/v1alpha1"
	"github.com/flightctl/flightctl/internal/service/catalog"
	"github.com/flightctl/flightctl/internal/service/vulnerabilityfinding"
)

type TransportHandler struct {
	catalog              catalog.Service
	vulnerabilityfinding vulnerabilityfinding.Service
	converter            convertv1alpha1.Converter
}

// Make sure we conform to servers Transport interface
var _ serverv1alpha1.Transport = (*TransportHandler)(nil)

func NewTransportHandler(catalogSvc catalog.Service, vulnerabilityfindingSvc vulnerabilityfinding.Service, converter convertv1alpha1.Converter) *TransportHandler {
	return &TransportHandler{
		catalog:              catalogSvc,
		vulnerabilityfinding: vulnerabilityfindingSvc,
		converter:            converter,
	}
}
