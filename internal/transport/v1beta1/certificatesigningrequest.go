package transportv1beta1

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (GET /api/v1/certificatesigningrequests)
func (h *TransportHandler) ListCertificateSigningRequests(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListCertificateSigningRequestsParams) {
	domainParams := h.converter.CertificateSigningRequest().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListCertificateSigningRequests(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.CertificateSigningRequest().ListFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (POST /api/v1/certificatesigningrequests)
func (h *TransportHandler) CreateCertificateSigningRequest(w http.ResponseWriter, r *http.Request) {
	var csr apiv1beta1.CertificateSigningRequest
	if err := json.NewDecoder(r.Body).Decode(&csr); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainCSR := h.converter.CertificateSigningRequest().ToDomain(csr)
	body, status := h.serviceHandler.CreateCertificateSigningRequest(r.Context(), transport.OrgIDFromContext(r.Context()), domainCSR)
	apiResult := h.converter.CertificateSigningRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/certificatesigningrequests/{name})
func (h *TransportHandler) DeleteCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteCertificateSigningRequest(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	h.SetResponse(w, nil, status)
}

// (GET /api/v1/certificatesigningrequests/{name})
func (h *TransportHandler) GetCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetCertificateSigningRequest(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.CertificateSigningRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/certificatesigningrequests/{name})
func (h *TransportHandler) PatchCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.serviceHandler.PatchCertificateSigningRequest(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.CertificateSigningRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/certificatesigningrequests/{name})
func (h *TransportHandler) ReplaceCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	var csr apiv1beta1.CertificateSigningRequest
	if err := json.NewDecoder(r.Body).Decode(&csr); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainCSR := h.converter.CertificateSigningRequest().ToDomain(csr)
	body, status := h.serviceHandler.ReplaceCertificateSigningRequest(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainCSR)
	apiResult := h.converter.CertificateSigningRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/certificatesigningrequests/{name}/approval)
func (h *TransportHandler) UpdateCertificateSigningRequestApproval(w http.ResponseWriter, r *http.Request, name string) {
	var csr apiv1beta1.CertificateSigningRequest
	if err := json.NewDecoder(r.Body).Decode(&csr); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainCSR := h.converter.CertificateSigningRequest().ToDomain(csr)
	body, status := h.serviceHandler.UpdateCertificateSigningRequestApproval(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainCSR)
	apiResult := h.converter.CertificateSigningRequest().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}
