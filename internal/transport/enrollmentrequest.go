package transport

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
)

// (POST /api/v1/enrollmentrequests)
func (h *TransportHandler) CreateEnrollmentRequest(w http.ResponseWriter, r *http.Request) {
	var er apiv1beta1.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	domainER := h.converter.V1beta1().EnrollmentRequest().ToDomain(er)
	body, status := h.serviceHandler.CreateEnrollmentRequest(r.Context(), OrgIDFromContext(r.Context()), domainER)
	apiResult := h.converter.V1beta1().EnrollmentRequest().FromDomain(body)
	SetResponse(w, apiResult, status)
}

// (GET /api/v1/enrollmentrequests)
func (h *TransportHandler) ListEnrollmentRequests(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListEnrollmentRequestsParams) {
	domainParams := h.converter.V1beta1().EnrollmentRequest().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListEnrollmentRequests(r.Context(), OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.V1beta1().EnrollmentRequest().ListFromDomain(body)
	SetResponse(w, apiResult, status)
}

// (GET /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) GetEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetEnrollmentRequest(r.Context(), OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.V1beta1().EnrollmentRequest().FromDomain(body)
	SetResponse(w, apiResult, status)
}

// (PUT /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) ReplaceEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	var er apiv1beta1.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	domainER := h.converter.V1beta1().EnrollmentRequest().ToDomain(er)
	body, status := h.serviceHandler.ReplaceEnrollmentRequest(r.Context(), OrgIDFromContext(r.Context()), name, domainER)
	apiResult := h.converter.V1beta1().EnrollmentRequest().FromDomain(body)
	SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) PatchEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.V1beta1().Common().PatchRequestToDomain(patch)
	body, status := h.serviceHandler.PatchEnrollmentRequest(r.Context(), OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.V1beta1().EnrollmentRequest().FromDomain(body)
	SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) DeleteEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteEnrollmentRequest(r.Context(), OrgIDFromContext(r.Context()), name)
	SetResponse(w, nil, status)
}

// (GET /api/v1/enrollmentrequests/{name}/status)
func (h *TransportHandler) GetEnrollmentRequestStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetEnrollmentRequestStatus(r.Context(), OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.V1beta1().EnrollmentRequest().FromDomain(body)
	SetResponse(w, apiResult, status)
}

// (PUT /api/v1/enrollmentrequests/{name}/approval)
func (h *TransportHandler) ApproveEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	var approval apiv1beta1.EnrollmentRequestApproval
	if err := json.NewDecoder(r.Body).Decode(&approval); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	domainApproval := h.converter.V1beta1().EnrollmentRequest().ApprovalToDomain(approval)
	body, status := h.serviceHandler.ApproveEnrollmentRequest(r.Context(), OrgIDFromContext(r.Context()), name, domainApproval)
	apiResult := h.converter.V1beta1().EnrollmentRequest().ApprovalStatusFromDomain(body)
	SetResponse(w, apiResult, status)
}

// (PUT /api/v1/enrollmentrequests/{name}/status)
func (h *TransportHandler) ReplaceEnrollmentRequestStatus(w http.ResponseWriter, r *http.Request, name string) {
	var er apiv1beta1.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	domainER := h.converter.V1beta1().EnrollmentRequest().ToDomain(er)
	body, status := h.serviceHandler.ReplaceEnrollmentRequestStatus(r.Context(), OrgIDFromContext(r.Context()), name, domainER)
	apiResult := h.converter.V1beta1().EnrollmentRequest().FromDomain(body)
	SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/enrollmentrequests/{name}/status)
func (h *TransportHandler) PatchEnrollmentRequestStatus(w http.ResponseWriter, r *http.Request, name string) {
	status := apiv1beta1.StatusNotImplemented("not yet implemented")
	SetResponse(w, nil, status)
}
