package service

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/api/server"
)

// (DELETE /api/v1/certificatesigningrequests)
func (h *ServiceHandler) DeleteCollectionCertificateSigningRequest(ctx context.Context, request server.DeleteCollectionCertificateSigningRequestRequestObject) (server.DeleteCollectionCertificateSigningRequestResponseObject, error) {
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

// (GET /api/v1/certificatesigningrequests/{name}/approval)
func (h *ServiceHandler) ReadCertificateSigningRequestApproval(ctx context.Context, request server.ReadCertificateSigningRequestApprovalRequestObject) (server.ReadCertificateSigningRequestApprovalResponseObject, error) {
	return nil, errors.New("Not implemented")
}

// (PATCH /api/v1/certificatesigningrequests/{name}/approval)
func (h *ServiceHandler) PatchCertificateSigningRequestApproval(ctx context.Context, request server.PatchCertificateSigningRequestApprovalRequestObject) (server.PatchCertificateSigningRequestApprovalResponseObject, error) {
	return nil, errors.New("Not implemented")
}

// (PUT /api/v1/certificatesigningrequests/{name}/approval)
func (h *ServiceHandler) ReplaceCertificateSigningRequestApproval(ctx context.Context, request server.ReplaceCertificateSigningRequestApprovalRequestObject) (server.ReplaceCertificateSigningRequestApprovalResponseObject, error) {
	return nil, errors.New("Not implemented")
}

// (GET /api/v1/certificatesigningrequests/{name}/status)
func (h *ServiceHandler) ReadCertificateSigningRequestStatus(ctx context.Context, request server.ReadCertificateSigningRequestStatusRequestObject) (server.ReadCertificateSigningRequestStatusResponseObject, error) {
	return nil, errors.New("Not implemented")
}

// (PATCH /api/v1/certificatesigningrequests/{name}/status)
func (h *ServiceHandler) PatchCertificateSigningRequestStatus(ctx context.Context, request server.PatchCertificateSigningRequestStatusRequestObject) (server.PatchCertificateSigningRequestStatusResponseObject, error) {
	return nil, errors.New("Not implemented")
}

// (PUT /api/v1/certificatesigningrequests/{name}/status)
func (h *ServiceHandler) ReplaceCertificateSigningRequestStatus(ctx context.Context, request server.ReplaceCertificateSigningRequestStatusRequestObject) (server.ReplaceCertificateSigningRequestStatusResponseObject, error) {
	return nil, errors.New("Not implemented")
}
