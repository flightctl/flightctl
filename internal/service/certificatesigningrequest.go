package service

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/api/server"
)

// (DELETE /api/v1/certificatesigningrequests)
func (h *ServiceHandler) DeleteCertificateSigningRequests(ctx context.Context, request server.DeleteCertificateSigningRequestsRequestObject) (server.DeleteCertificateSigningRequestsResponseObject, error) {
	return nil, errors.New("not implemented")
}

// (GET /api/v1/certificatesigningrequests)
func (h *ServiceHandler) ListCertificateSigningRequests(ctx context.Context, request server.ListCertificateSigningRequestsRequestObject) (server.ListCertificateSigningRequestsResponseObject, error) {
	return nil, errors.New("not implemented")
}

// (POST /api/v1/certificatesigningrequests)
func (h *ServiceHandler) CreateCertificateSigningRequest(ctx context.Context, request server.CreateCertificateSigningRequestRequestObject) (server.CreateCertificateSigningRequestResponseObject, error) {
	return nil, errors.New("not implemented")
}

// (DELETE /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) DeleteCertificateSigningRequest(ctx context.Context, request server.DeleteCertificateSigningRequestRequestObject) (server.DeleteCertificateSigningRequestResponseObject, error) {
	return nil, errors.New("not implemented")
}

// (GET /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) ReadCertificateSigningRequest(ctx context.Context, request server.ReadCertificateSigningRequestRequestObject) (server.ReadCertificateSigningRequestResponseObject, error) {
	return nil, errors.New("not implemented")
}

// (PATCH /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) PatchCertificateSigningRequest(ctx context.Context, request server.PatchCertificateSigningRequestRequestObject) (server.PatchCertificateSigningRequestResponseObject, error) {
	return nil, errors.New("Not implemented")
}

// (PUT /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) ReplaceCertificateSigningRequest(ctx context.Context, request server.ReplaceCertificateSigningRequestRequestObject) (server.ReplaceCertificateSigningRequestResponseObject, error) {
	return nil, errors.New("not implemented")
}

// (POST /api/v1/certificatesigningrequests/{name}/approval)
func (h *ServiceHandler) ApproveCertificateSigningRequest(ctx context.Context, request server.ApproveCertificateSigningRequestRequestObject) (server.ApproveCertificateSigningRequestResponseObject, error) {
	return nil, errors.New("Not implemented")
}

// (DELETE /api/v1/certificatesigningrequests/{name}/approval)
func (h *ServiceHandler) DenyCertificateSigningRequest(ctx context.Context, request server.DenyCertificateSigningRequestRequestObject) (server.DenyCertificateSigningRequestResponseObject, error) {
	return nil, errors.New("Not implemented")
}
