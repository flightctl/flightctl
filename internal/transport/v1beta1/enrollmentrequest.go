package transportv1beta1

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/transport"
)

// (POST /api/v1/enrollmentrequests)
func (h *TransportHandler) CreateEnrollmentRequest(w http.ResponseWriter, r *http.Request) {
	var er apiv1beta1.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainER := h.converter.EnrollmentRequest().ToDomain(er)
	body, status := enrollmentrequestservice.CreateEnrollmentRequestFromUntrusted(r.Context(), h.enrollmentrequest, transport.OrgIDFromContext(r.Context()), domainER)
	apiResult := h.converter.EnrollmentRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/enrollmentrequests)
func (h *TransportHandler) ListEnrollmentRequests(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListEnrollmentRequestsParams) {
	domainParams := h.converter.EnrollmentRequest().ListParamsToDomain(params)
	body, status := h.enrollmentrequest.ListEnrollmentRequests(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.EnrollmentRequest().ListFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) GetEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.enrollmentrequest.GetEnrollmentRequest(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.EnrollmentRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) ReplaceEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	var er apiv1beta1.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainER := h.converter.EnrollmentRequest().ToDomain(er)
	body, status := enrollmentrequestservice.ReplaceEnrollmentRequestFromUntrusted(r.Context(), h.enrollmentrequest, transport.OrgIDFromContext(r.Context()), name, domainER)
	apiResult := h.converter.EnrollmentRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) PatchEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.enrollmentrequest.PatchEnrollmentRequest(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.EnrollmentRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/enrollmentrequests/{name})
func (h *TransportHandler) DeleteEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	status := h.enrollmentrequest.DeleteEnrollmentRequest(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	h.SetResponse(w, nil, status)
}

// (GET /api/v1/enrollmentrequests/{name}/status)
func (h *TransportHandler) GetEnrollmentRequestStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.enrollmentrequest.GetEnrollmentRequestStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.EnrollmentRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/enrollmentrequests/{name}/approval)
func (h *TransportHandler) ApproveEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	var approval apiv1beta1.EnrollmentRequestApproval
	if err := json.NewDecoder(r.Body).Decode(&approval); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainApproval := h.converter.EnrollmentRequest().ApprovalToDomain(approval)
	body, status := h.enrollmentrequest.ApproveEnrollmentRequest(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainApproval)
	apiResult := h.converter.EnrollmentRequest().ApprovalStatusFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/enrollmentrequests/{name}/status)
func (h *TransportHandler) ReplaceEnrollmentRequestStatus(w http.ResponseWriter, r *http.Request, name string) {
	var er apiv1beta1.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainER := h.converter.EnrollmentRequest().ToDomain(er)
	body, status := h.enrollmentrequest.ReplaceEnrollmentRequestStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainER)
	apiResult := h.converter.EnrollmentRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/enrollmentrequests/{name}/status)
func (h *TransportHandler) PatchEnrollmentRequestStatus(w http.ResponseWriter, r *http.Request, name string) {
	status := apiv1beta1.StatusNotImplemented("not yet implemented")
	h.SetResponse(w, nil, status)
}
