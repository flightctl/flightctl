package transport

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
)

// (POST /api/v1/resourcesyncs)
func (h *TransportHandler) CreateResourceSync(w http.ResponseWriter, r *http.Request) {
	var rs apiv1beta1.ResourceSync
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	domainRS := h.converter.V1beta1().ResourceSync().ToDomain(rs)
	body, status := h.serviceHandler.CreateResourceSync(r.Context(), OrgIDFromContext(r.Context()), domainRS)
	apiResult := h.converter.V1beta1().ResourceSync().FromDomain(body)
	SetResponse(w, apiResult, status)
}

// (GET /api/v1/resourcesyncs)
func (h *TransportHandler) ListResourceSyncs(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListResourceSyncsParams) {
	domainParams := h.converter.V1beta1().ResourceSync().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListResourceSyncs(r.Context(), OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.V1beta1().ResourceSync().ListFromDomain(body)
	SetResponse(w, apiResult, status)
}

// (GET /api/v1/resourcesyncs/{name})
func (h *TransportHandler) GetResourceSync(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetResourceSync(r.Context(), OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.V1beta1().ResourceSync().FromDomain(body)
	SetResponse(w, apiResult, status)
}

// (PUT /api/v1/resourcesyncs/{name})
func (h *TransportHandler) ReplaceResourceSync(w http.ResponseWriter, r *http.Request, name string) {
	var rs apiv1beta1.ResourceSync
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	domainRS := h.converter.V1beta1().ResourceSync().ToDomain(rs)
	body, status := h.serviceHandler.ReplaceResourceSync(r.Context(), OrgIDFromContext(r.Context()), name, domainRS)
	apiResult := h.converter.V1beta1().ResourceSync().FromDomain(body)
	SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/resourcesyncs/{name})
func (h *TransportHandler) DeleteResourceSync(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteResourceSync(r.Context(), OrgIDFromContext(r.Context()), name)
	SetResponse(w, nil, status)
}

// (PATCH /api/v1/resourcesyncs/{name})
func (h *TransportHandler) PatchResourceSync(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.V1beta1().Common().PatchRequestToDomain(patch)
	body, status := h.serviceHandler.PatchResourceSync(r.Context(), OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.V1beta1().ResourceSync().FromDomain(body)
	SetResponse(w, apiResult, status)
}
