package transportv1beta1

import (
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (GET api/v1/fleets/{fleet}/templateVersions)
func (h *TransportHandler) ListTemplateVersions(w http.ResponseWriter, r *http.Request, fleet string, params apiv1beta1.ListTemplateVersionsParams) {
	domainParams := h.converter.TemplateVersion().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListTemplateVersions(r.Context(), transport.OrgIDFromContext(r.Context()), fleet, domainParams)
	apiResult := h.converter.TemplateVersion().ListFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *TransportHandler) GetTemplateVersion(w http.ResponseWriter, r *http.Request, fleet string, name string) {
	body, status := h.serviceHandler.GetTemplateVersion(r.Context(), transport.OrgIDFromContext(r.Context()), fleet, name)
	apiResult := h.converter.TemplateVersion().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *TransportHandler) DeleteTemplateVersion(w http.ResponseWriter, r *http.Request, fleet string, name string) {
	status := h.serviceHandler.DeleteTemplateVersion(r.Context(), transport.OrgIDFromContext(r.Context()), fleet, name)
	transport.SetResponse(w, nil, status)
}
