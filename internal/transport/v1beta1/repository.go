package transportv1beta1

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (POST /api/v1/repositories)
func (h *TransportHandler) CreateRepository(w http.ResponseWriter, r *http.Request) {
	var rs apiv1beta1.Repository
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainRepo := h.converter.Repository().ToDomain(rs)
	body, status := h.serviceHandler.CreateRepository(r.Context(), transport.OrgIDFromContext(r.Context()), domainRepo)
	apiResult := h.converter.Repository().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/repositories)
func (h *TransportHandler) ListRepositories(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListRepositoriesParams) {
	domainParams := h.converter.Repository().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListRepositories(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.Repository().ListFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/repositories/{name})
func (h *TransportHandler) GetRepository(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetRepository(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Repository().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/repositories/{name})
func (h *TransportHandler) ReplaceRepository(w http.ResponseWriter, r *http.Request, name string) {
	var rs apiv1beta1.Repository
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainRepo := h.converter.Repository().ToDomain(rs)
	body, status := h.serviceHandler.ReplaceRepository(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainRepo)
	apiResult := h.converter.Repository().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/repositories/{name})
func (h *TransportHandler) DeleteRepository(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteRepository(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	transport.SetResponse(w, nil, status)
}

// (PATCH /api/v1/repositories/{name})
func (h *TransportHandler) PatchRepository(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.serviceHandler.PatchRepository(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.Repository().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}
