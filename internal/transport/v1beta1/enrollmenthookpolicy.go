package transportv1beta1

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	enrollmenthookpolicyservice "github.com/flightctl/flightctl/internal/service/enrollmenthookpolicy"
	"github.com/flightctl/flightctl/internal/transport"
)

// (POST /api/v1/enrollmenthookpolicies)
func (h *TransportHandler) CreateEnrollmentHookPolicy(w http.ResponseWriter, r *http.Request) {
	var p apiv1beta1.EnrollmentHookPolicy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPolicy := h.converter.EnrollmentHookPolicy().ToDomain(p)
	body, status := enrollmenthookpolicyservice.CreateEnrollmentHookPolicyFromUntrusted(r.Context(), h.enrollmenthookpolicy, transport.OrgIDFromContext(r.Context()), domainPolicy)
	apiResult := h.converter.EnrollmentHookPolicy().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/enrollmenthookpolicies)
func (h *TransportHandler) ListEnrollmentHookPolicies(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListEnrollmentHookPoliciesParams) {
	domainParams := h.converter.EnrollmentHookPolicy().ListParamsToDomain(params)
	body, status := h.enrollmenthookpolicy.ListEnrollmentHookPolicies(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.EnrollmentHookPolicy().ListFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/enrollmenthookpolicies/{name})
func (h *TransportHandler) GetEnrollmentHookPolicy(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.enrollmenthookpolicy.GetEnrollmentHookPolicy(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.EnrollmentHookPolicy().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/enrollmenthookpolicies/{name})
func (h *TransportHandler) ReplaceEnrollmentHookPolicy(w http.ResponseWriter, r *http.Request, name string) {
	var p apiv1beta1.EnrollmentHookPolicy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPolicy := h.converter.EnrollmentHookPolicy().ToDomain(p)
	body, status := enrollmenthookpolicyservice.ReplaceEnrollmentHookPolicyFromUntrusted(r.Context(), h.enrollmenthookpolicy, transport.OrgIDFromContext(r.Context()), name, domainPolicy)
	apiResult := h.converter.EnrollmentHookPolicy().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/enrollmenthookpolicies/{name})
func (h *TransportHandler) DeleteEnrollmentHookPolicy(w http.ResponseWriter, r *http.Request, name string) {
	status := h.enrollmenthookpolicy.DeleteEnrollmentHookPolicy(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	h.SetResponse(w, nil, status)
}

// (PATCH /api/v1/enrollmenthookpolicies/{name})
func (h *TransportHandler) PatchEnrollmentHookPolicy(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.enrollmenthookpolicy.PatchEnrollmentHookPolicy(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.EnrollmentHookPolicy().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}
