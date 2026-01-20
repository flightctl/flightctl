package transportv1beta1

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (POST /api/v1/authproviders)
func (h *TransportHandler) CreateAuthProvider(w http.ResponseWriter, r *http.Request) {
	var authProvider apiv1beta1.AuthProvider
	if err := json.NewDecoder(r.Body).Decode(&authProvider); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainAP := h.converter.AuthProvider().ToDomain(authProvider)
	body, status := h.serviceHandler.CreateAuthProvider(r.Context(), transport.OrgIDFromContext(r.Context()), domainAP)
	apiResult := h.converter.AuthProvider().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/authproviders)
func (h *TransportHandler) ListAuthProviders(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListAuthProvidersParams) {
	domainParams := h.converter.AuthProvider().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListAuthProviders(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.AuthProvider().ListFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/authproviders/{name})
func (h *TransportHandler) GetAuthProvider(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetAuthProvider(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.AuthProvider().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/authproviders/{name})
func (h *TransportHandler) ReplaceAuthProvider(w http.ResponseWriter, r *http.Request, name string) {
	var authProvider apiv1beta1.AuthProvider
	if err := json.NewDecoder(r.Body).Decode(&authProvider); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainAP := h.converter.AuthProvider().ToDomain(authProvider)
	body, status := h.serviceHandler.ReplaceAuthProvider(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainAP)
	apiResult := h.converter.AuthProvider().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/authproviders/{name})
func (h *TransportHandler) PatchAuthProvider(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.serviceHandler.PatchAuthProvider(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.AuthProvider().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/authproviders/{name})
func (h *TransportHandler) DeleteAuthProvider(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteAuthProvider(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	transport.SetResponse(w, nil, status)
}
