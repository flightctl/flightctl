package service

import (
	"context"
	"encoding/base64"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
)

// (GET /api/v1/enrollmentconfig)
func (h *ServiceHandler) EnrollmentConfig(ctx context.Context, request server.EnrollmentConfigRequestObject) (server.EnrollmentConfigResponseObject, error) {
	orgId := store.NullOrgId

	csr, err := h.store.CertificateSigningRequest().Get(ctx, orgId, request.Name)
	if err != nil {
		switch err {
		case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
			return server.EnrollmentConfig400JSONResponse{Message: err.Error()}, nil
		case flterrors.ErrResourceNotFound:
			return server.EnrollmentConfig404JSONResponse{}, nil
		default:
			return nil, err
		}
	}

	if csr.Status == nil || csr.Status.Certificate == nil {
		return server.EnrollmentConfig400JSONResponse{Message: "CSR is not signed"}, nil
	}

	cert, _, err := h.ca.Config.GetPEMBytes()
	if err != nil {
		return server.EnrollmentConfig400JSONResponse{Message: err.Error()}, nil
	}
	return server.EnrollmentConfig200JSONResponse{
		EnrollmentService: v1alpha1.EnrollmentService{
			Authentication: v1alpha1.EnrollmentServiceAuth{
				ClientCertificateData: base64.StdEncoding.EncodeToString(*csr.Status.Certificate),
				ClientKeyData:         "",
			},
			Service: v1alpha1.EnrollmentServiceService{
				CertificateAuthorityData: base64.StdEncoding.EncodeToString(cert),
				Server:                   h.agentEndpoint,
			},
			EnrollmentUiEndpoint: h.uiUrl,
		},
	}, nil
}
