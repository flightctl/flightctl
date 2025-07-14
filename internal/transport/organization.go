package transport

import (
	"net/http"
)

// (GET /api/v1/users/me/organizations)
func (h *TransportHandler) ListUserOrganizations(w http.ResponseWriter, r *http.Request) {
	body, status := h.serviceHandler.ListUserOrganizations(r.Context())
	SetResponse(w, body, status)
}
