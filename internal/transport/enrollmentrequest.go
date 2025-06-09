package transport

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// (POST /api/v1/enrollmentrequests)
func (h *TransportHandler) CreateEnrollmentRequest(w http.ResponseWriter, r *http.Request) {
	var er api.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.CreateEnrollmentRequest(r.Context(), er)
	SetResponse(w, body, status)
}

// (GET /api/v1/enrollmentrequests)
func (h *TransportHandler) ListEnrollmentRequests(w http.ResponseWriter, r *http.Request, params api.ListEnrollmentRequestsParams) {
	body, status := h.serviceHandler.ListEnrollmentRequests(r.Context(), params)
	SetResponse(w, body, status)
}

// (GET /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) GetEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetEnrollmentRequest(r.Context(), name)
	SetResponse(w, body, status)
}

// (PUT /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) ReplaceEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	var er api.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceEnrollmentRequest(r.Context(), name, er)
	SetResponse(w, body, status)
}

// (PATCH /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) PatchEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.PatchEnrollmentRequest(r.Context(), name, patch)
	SetResponse(w, body, status)
}

// (DELETE /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) DeleteEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteEnrollmentRequest(r.Context(), name)
	SetResponse(w, nil, status)
}

// (GET /api/v1/enrollmentrequests/{name}/status)
func (h *TransportHandler) GetEnrollmentRequestStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetEnrollmentRequestStatus(r.Context(), name)
	SetResponse(w, body, status)
}

// (POST /api/v1/enrollmentrequests/{name}/approval)
func (h *TransportHandler) ApproveEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	var approval api.EnrollmentRequestApproval
	if err := json.NewDecoder(r.Body).Decode(&approval); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ApproveEnrollmentRequest(r.Context(), name, approval)
	SetResponse(w, body, status)
}

// (PUT /api/v1/enrollmentrequests/{name}/status)
func (h *TransportHandler) ReplaceEnrollmentRequestStatus(w http.ResponseWriter, r *http.Request, name string) {
	var er api.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceEnrollmentRequestStatus(r.Context(), name, er)
	SetResponse(w, body, status)
}

// (PATCH /api/v1/enrollmentrequests/{name}/status)
func (h *TransportHandler) PatchEnrollmentRequestStatus(w http.ResponseWriter, r *http.Request, name string) {
	status := api.StatusNotImplemented("not yet implemented")
	SetResponse(w, nil, status)
}
