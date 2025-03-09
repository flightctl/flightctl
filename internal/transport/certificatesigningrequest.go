package transport

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// (DELETE /api/v1/certificatesigningrequests)
func (h *TransportHandler) DeleteCertificateSigningRequests(w http.ResponseWriter, r *http.Request) {
	status := h.serviceHandler.DeleteCertificateSigningRequests(r.Context())
	SetResponse(w, nil, status)
}

// (GET /api/v1/certificatesigningrequests)
func (h *TransportHandler) ListCertificateSigningRequests(w http.ResponseWriter, r *http.Request, params api.ListCertificateSigningRequestsParams) {
	body, status := h.serviceHandler.ListCertificateSigningRequests(r.Context(), params)
	SetResponse(w, body, status)
}

// (POST /api/v1/certificatesigningrequests)
func (h *TransportHandler) CreateCertificateSigningRequest(w http.ResponseWriter, r *http.Request) {
	var csr api.CertificateSigningRequest
	if err := json.NewDecoder(r.Body).Decode(&csr); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.CreateCertificateSigningRequest(r.Context(), csr)
	SetResponse(w, body, status)
}

// (DELETE /api/v1/certificatesigningrequests/{name})
func (h *TransportHandler) DeleteCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteCertificateSigningRequest(r.Context(), name)
	SetResponse(w, nil, status)
}

// (GET /api/v1/certificatesigningrequests/{name})
func (h *TransportHandler) GetCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetCertificateSigningRequest(r.Context(), name)
	SetResponse(w, body, status)
}

// (PATCH /api/v1/certificatesigningrequests/{name})
func (h *TransportHandler) PatchCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.PatchCertificateSigningRequest(r.Context(), name, patch)
	SetResponse(w, body, status)
}

// (PUT /api/v1/certificatesigningrequests/{name})
func (h *TransportHandler) ReplaceCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	var csr api.CertificateSigningRequest
	if err := json.NewDecoder(r.Body).Decode(&csr); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceCertificateSigningRequest(r.Context(), name, csr)
	SetResponse(w, body, status)
}

// (PUT /api/v1/certificatesigningrequests/{name}/approval)
func (h *TransportHandler) UpdateCertificateSigningRequestApproval(w http.ResponseWriter, r *http.Request, name string) {
	var csr api.CertificateSigningRequest
	if err := json.NewDecoder(r.Body).Decode(&csr); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceCertificateSigningRequest(r.Context(), name, csr)
	SetResponse(w, body, status)
}
