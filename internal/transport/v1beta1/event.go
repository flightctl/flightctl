package transportv1beta1

import (
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (GET /api/v1/events)
func (h *TransportHandler) ListEvents(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListEventsParams) {
	domainParams := h.converter.Event().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListEvents(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.Event().ListFromDomain(body)
	h.SetResponse(w, apiResult, status)
}
