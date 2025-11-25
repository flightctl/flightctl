package transport

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/v1beta1"
)

// (GET /api/v1/events)
func (h *TransportHandler) ListEvents(w http.ResponseWriter, r *http.Request, params api.ListEventsParams) {
	body, status := h.serviceHandler.ListEvents(r.Context(), OrgIDFromContext(r.Context()), params)
	SetResponse(w, body, status)
}
