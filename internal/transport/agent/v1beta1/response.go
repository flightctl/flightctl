package agenttransportv1beta1

import (
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/transport"
)

// SetResponse writes an HTTP response, converting domain.Status to the
// v1beta1 API Status via the handler's converter.
// For 2xx responses, body is encoded; for non-2xx, the converted status is encoded.
func (s *AgentTransportHandler) SetResponse(w http.ResponseWriter, body any, status domain.Status) {
	apiStatus := s.converter.Common().StatusFromDomain(status)
	transport.WriteJSONResponse(w, body, apiStatus, int(status.Code))
}

// SetParseFailureResponse writes a 500 response for JSON decode failures.
func (s *AgentTransportHandler) SetParseFailureResponse(w http.ResponseWriter, err error) {
	s.SetResponse(w, nil, domain.StatusInternalServerError(fmt.Sprintf("can't decode JSON body: %v", err)))
}
