package transport

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/v1beta1"
)

// (GET /api/v1/labels)
func (h *TransportHandler) ListLabels(w http.ResponseWriter, r *http.Request, params api.ListLabelsParams) {
	body, status := h.serviceHandler.ListLabels(r.Context(), OrgIDFromContext(r.Context()), params)
	SetResponse(w, body, status)
}
