package transport

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// (POST /api/v1/repositories)
func (h *TransportHandler) CreateRepository(w http.ResponseWriter, r *http.Request) {
	var rs api.Repository
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.CreateRepository(r.Context(), rs)
	SetResponse(w, body, status)
}

// (GET /api/v1/repositories)
func (h *TransportHandler) ListRepositories(w http.ResponseWriter, r *http.Request, params api.ListRepositoriesParams) {
	body, status := h.serviceHandler.ListRepositories(r.Context(), params)
	SetResponse(w, body, status)
}

// (GET /api/v1/repositories/{name})
func (h *TransportHandler) GetRepository(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetRepository(r.Context(), name)
	SetResponse(w, body, status)
}

// (PUT /api/v1/repositories/{name})
func (h *TransportHandler) ReplaceRepository(w http.ResponseWriter, r *http.Request, name string) {
	var rs api.Repository
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceRepository(r.Context(), name, rs)
	SetResponse(w, body, status)
}

// (DELETE /api/v1/repositories/{name})
func (h *TransportHandler) DeleteRepository(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteRepository(r.Context(), name)
	SetResponse(w, nil, status)
}

// (PATCH /api/v1/repositories/{name})
func (h *TransportHandler) PatchRepository(w http.ResponseWriter, r *http.Request, name string) {
	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.PatchRepository(r.Context(), name, patch)
	SetResponse(w, body, status)
}
