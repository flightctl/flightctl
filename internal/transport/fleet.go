package transport

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// (POST /api/v1/fleets)
func (h *TransportHandler) CreateFleet(w http.ResponseWriter, r *http.Request) {
	var fleet api.Fleet
	if err := json.NewDecoder(r.Body).Decode(&fleet); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.CreateFleet(r.Context(), fleet)
	SetResponse(w, body, status)
}

// (GET /api/v1/fleets)
func (h *TransportHandler) ListFleets(w http.ResponseWriter, r *http.Request, params api.ListFleetsParams) {
	body, status := h.serviceHandler.ListFleets(r.Context(), params)
	SetResponse(w, body, status)
}

// (GET /api/v1/fleets/{name})
func (h *TransportHandler) GetFleet(w http.ResponseWriter, r *http.Request, name string, params api.GetFleetParams) {
	body, status := h.serviceHandler.GetFleet(r.Context(), name, params)
	SetResponse(w, body, status)
}

// (PUT /api/v1/fleets/{name})
func (h *TransportHandler) ReplaceFleet(w http.ResponseWriter, r *http.Request, name string) {
	var fleet api.Fleet
	if err := json.NewDecoder(r.Body).Decode(&fleet); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceFleet(r.Context(), name, fleet)
	SetResponse(w, body, status)
}

// (DELETE /api/v1/fleets/{name})
func (h *TransportHandler) DeleteFleet(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteFleet(r.Context(), name)
	SetResponse(w, nil, status)
}

// (GET /api/v1/fleets/{name}/status)
func (h *TransportHandler) GetFleetStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetFleetStatus(r.Context(), name)
	SetResponse(w, body, status)
}

// (PUT /api/v1/fleets/{name}/status)
func (h *TransportHandler) ReplaceFleetStatus(w http.ResponseWriter, r *http.Request, name string) {
	var fleet api.Fleet
	if err := json.NewDecoder(r.Body).Decode(&fleet); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceFleetStatus(r.Context(), name, fleet)
	SetResponse(w, body, status)
}

// (PATCH /api/v1/fleets/{name})
func (h *TransportHandler) PatchFleet(w http.ResponseWriter, r *http.Request, name string) {
	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.PatchFleet(r.Context(), name, patch)
	SetResponse(w, body, status)
}

// (PATCH /api/v1/fleets/{name}/status)
func (h *TransportHandler) PatchFleetStatus(w http.ResponseWriter, r *http.Request, name string) {
	status := api.StatusNotImplemented("not yet implemented")
	SetResponse(w, nil, status)
}
