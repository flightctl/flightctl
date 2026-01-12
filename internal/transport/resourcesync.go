package transport

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
)

// (POST /api/v1/resourcesyncs)
func (h *TransportHandler) CreateResourceSync(w http.ResponseWriter, r *http.Request) {
	var rs api.ResourceSync
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.CreateResourceSync(r.Context(), OrgIDFromContext(r.Context()), rs)
	SetResponse(w, body, status)
}

// (GET /api/v1/resourcesyncs)
func (h *TransportHandler) ListResourceSyncs(w http.ResponseWriter, r *http.Request, params api.ListResourceSyncsParams) {
	body, status := h.serviceHandler.ListResourceSyncs(r.Context(), OrgIDFromContext(r.Context()), params)
	SetResponse(w, body, status)
}

// (GET /api/v1/resourcesyncs/{name})
func (h *TransportHandler) GetResourceSync(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetResourceSync(r.Context(), OrgIDFromContext(r.Context()), name)
	SetResponse(w, body, status)
}

// (PUT /api/v1/resourcesyncs/{name})
func (h *TransportHandler) ReplaceResourceSync(w http.ResponseWriter, r *http.Request, name string) {
	var rs api.ResourceSync
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceResourceSync(r.Context(), OrgIDFromContext(r.Context()), name, rs)
	SetResponse(w, body, status)
}

// (DELETE /api/v1/resourcesyncs/{name})
func (h *TransportHandler) DeleteResourceSync(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteResourceSync(r.Context(), OrgIDFromContext(r.Context()), name)
	SetResponse(w, nil, status)
}

// (PATCH /api/v1/resourcesyncs/{name})
func (h *TransportHandler) PatchResourceSync(w http.ResponseWriter, r *http.Request, name string) {
	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.PatchResourceSync(r.Context(), OrgIDFromContext(r.Context()), name, patch)
	SetResponse(w, body, status)
}
