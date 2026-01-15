package transport

import (
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
)

// (GET /api/v1/events)
func (h *TransportHandler) ListEvents(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListEventsParams) {
	domainParams := h.converter.V1beta1().Event().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListEvents(r.Context(), OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.V1beta1().Event().ListFromDomain(body)
	SetResponse(w, apiResult, status)
}
