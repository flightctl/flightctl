package v1

import (
	"github.com/flightctl/flightctl/internal/api/v1server"
	"github.com/flightctl/flightctl/internal/service"
)

// TransportHandler implements v1server.ServerInterface for v1 API requests.
// It translates v1 requests to v1beta1, calls the service, and translates responses back.
type TransportHandler struct {
	serviceHandler service.Service
}

// Ensure TransportHandler implements ServerInterface
var _ v1server.ServerInterface = (*TransportHandler)(nil)

// NewTransportHandler creates a new v1 TransportHandler.
func NewTransportHandler(serviceHandler service.Service) *TransportHandler {
	return &TransportHandler{
		serviceHandler: serviceHandler,
	}
}
