package transportv1beta1

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (POST /api/v1/fleets)
func (h *TransportHandler) CreateFleet(w http.ResponseWriter, r *http.Request) {
	var fleet apiv1beta1.Fleet
	if err := json.NewDecoder(r.Body).Decode(&fleet); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainFleet := h.converter.Fleet().ToDomain(fleet)
	body, status := h.serviceHandler.CreateFleet(r.Context(), transport.OrgIDFromContext(r.Context()), domainFleet)
	apiResult := h.converter.Fleet().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/fleets)
func (h *TransportHandler) ListFleets(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListFleetsParams) {
	domainParams := h.converter.Fleet().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListFleets(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.Fleet().ListFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/fleets/{name})
func (h *TransportHandler) GetFleet(w http.ResponseWriter, r *http.Request, name string, params apiv1beta1.GetFleetParams) {
	domainParams := h.converter.Fleet().GetParamsToDomain(params)
	body, status := h.serviceHandler.GetFleet(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainParams)
	apiResult := h.converter.Fleet().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/fleets/{name})
func (h *TransportHandler) ReplaceFleet(w http.ResponseWriter, r *http.Request, name string) {
	var fleet apiv1beta1.Fleet
	if err := json.NewDecoder(r.Body).Decode(&fleet); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainFleet := h.converter.Fleet().ToDomain(fleet)
	body, status := h.serviceHandler.ReplaceFleet(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainFleet)
	apiResult := h.converter.Fleet().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/fleets/{name})
func (h *TransportHandler) DeleteFleet(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteFleet(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	transport.SetResponse(w, nil, status)
}

// (GET /api/v1/fleets/{name}/status)
func (h *TransportHandler) GetFleetStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetFleetStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Fleet().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/fleets/{name}/status)
func (h *TransportHandler) ReplaceFleetStatus(w http.ResponseWriter, r *http.Request, name string) {
	var fleet apiv1beta1.Fleet
	if err := json.NewDecoder(r.Body).Decode(&fleet); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainFleet := h.converter.Fleet().ToDomain(fleet)
	body, status := h.serviceHandler.ReplaceFleetStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainFleet)
	apiResult := h.converter.Fleet().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/fleets/{name})
func (h *TransportHandler) PatchFleet(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.serviceHandler.PatchFleet(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.Fleet().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/fleets/{name}/status)
func (h *TransportHandler) PatchFleetStatus(w http.ResponseWriter, r *http.Request, name string) {
	status := apiv1beta1.StatusNotImplemented("not yet implemented")
	transport.SetResponse(w, nil, status)
}
