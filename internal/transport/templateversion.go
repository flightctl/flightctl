package transport

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// (GET api/v1/fleets/{fleet}/templateVersions)
func (h *TransportHandler) ListTemplateVersions(w http.ResponseWriter, r *http.Request, fleet string, params api.ListTemplateVersionsParams) {
	body, status := h.serviceHandler.ListTemplateVersions(r.Context(), fleet, params)
	SetResponse(w, body, status)
}

// (GET /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *TransportHandler) GetTemplateVersion(w http.ResponseWriter, r *http.Request, fleet string, name string) {
	body, status := h.serviceHandler.GetTemplateVersion(r.Context(), fleet, name)
	SetResponse(w, body, status)
}

// (DELETE /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *TransportHandler) DeleteTemplateVersion(w http.ResponseWriter, r *http.Request, fleet string, name string) {
	status := h.serviceHandler.DeleteTemplateVersion(r.Context(), fleet, name)
	SetResponse(w, nil, status)
}
