package transport

import (
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
)

// (GET api/v1/fleets/{fleet}/templateVersions)
func (h *TransportHandler) ListTemplateVersions(w http.ResponseWriter, r *http.Request, fleet string, params apiv1beta1.ListTemplateVersionsParams) {
	domainParams := h.converter.V1beta1().TemplateVersion().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListTemplateVersions(r.Context(), OrgIDFromContext(r.Context()), fleet, domainParams)
	apiResult := h.converter.V1beta1().TemplateVersion().ListFromDomain(body)
	SetResponse(w, apiResult, status)
}

// (GET /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *TransportHandler) GetTemplateVersion(w http.ResponseWriter, r *http.Request, fleet string, name string) {
	body, status := h.serviceHandler.GetTemplateVersion(r.Context(), OrgIDFromContext(r.Context()), fleet, name)
	apiResult := h.converter.V1beta1().TemplateVersion().FromDomain(body)
	SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *TransportHandler) DeleteTemplateVersion(w http.ResponseWriter, r *http.Request, fleet string, name string) {
	status := h.serviceHandler.DeleteTemplateVersion(r.Context(), OrgIDFromContext(r.Context()), fleet, name)
	SetResponse(w, nil, status)
}
