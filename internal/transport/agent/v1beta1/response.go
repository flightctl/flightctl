package agenttransportv1beta1

import (
	"encoding/base64"
	"errors"
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

// SetParseFailureResponse writes an error response for JSON decode failures.
func (s *AgentTransportHandler) SetParseFailureResponse(w http.ResponseWriter, err error) {
	// kin-openapi's format:byte regex allows base64url chars (-_) that Go's
	// encoding/json rejects when decoding []byte fields (strict standard base64).
	// This can be removed once kin-openapi supports OpenAPI 3.1 contentEncoding: base64,
	// which would validate base64 at the middleware layer before reaching json.Decode.
	var b64err base64.CorruptInputError
	if errors.As(err, &b64err) {
		s.SetResponse(w, nil, domain.StatusBadRequest(fmt.Sprintf("can't decode JSON body: %v", err)))
		return
	}
	// A decode failure not caught by OpenAPI validation indicates an uncaught
	// validation gap -- report as 500 so it surfaces for investigation.
	s.SetResponse(w, nil, domain.StatusInternalServerError(fmt.Sprintf("can't decode JSON body: %v", err)))
}
