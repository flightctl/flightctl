package transport

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
)

// (GET /api/v1/organizations)
func (h *TransportHandler) ListOrganizations(w http.ResponseWriter, r *http.Request, params api.ListOrganizationsParams) {
	body, status := h.serviceHandler.ListOrganizations(r.Context(), params)
	SetResponse(w, body, status)
}
