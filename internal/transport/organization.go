package transport

import (
	"net/http"
)

// (GET /api/v1/organizations)
func (h *TransportHandler) ListOrganizations(w http.ResponseWriter, r *http.Request) {
	body, status := h.serviceHandler.ListOrganizations(r.Context())
	SetResponse(w, body, status)
}
