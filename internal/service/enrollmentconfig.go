package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util/validation"
)

// (GET /api/v1/enrollmentconfig)
func (h *ServiceHandler) GetEnrollmentConfig(ctx context.Context, request server.GetEnrollmentConfigRequestObject) (server.GetEnrollmentConfigResponseObject, error) {
	orgId := store.NullOrgId

	caCert, _, err := h.ca.Config.GetPEMBytes()
	if err != nil {
		return nil, fmt.Errorf("failed to get CA certificate")
	}

	clientCert := []byte{}
	if request.Params.Csr != nil {
		if errs := validation.ValidateResourceName(request.Params.Csr); len(errs) > 0 {
			return server.GetEnrollmentConfig400JSONResponse{Message: errors.Join(errs...).Error()}, nil
		}

		csr, err := h.store.CertificateSigningRequest().Get(ctx, orgId, *request.Params.Csr)
		if err != nil {
			switch {
			case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
				return server.GetEnrollmentConfig400JSONResponse{Message: err.Error()}, nil
			case errors.Is(err, flterrors.ErrResourceNotFound):
				return server.GetEnrollmentConfig404JSONResponse{}, nil
			default:
				return nil, err
			}
		}

		if csr.Status != nil && csr.Status.Certificate != nil {
			clientCert = *csr.Status.Certificate
		}
	}

	return server.GetEnrollmentConfig200JSONResponse{
		EnrollmentService: v1alpha1.EnrollmentService{
			Authentication: v1alpha1.EnrollmentServiceAuth{
				ClientCertificateData: base64.StdEncoding.EncodeToString(clientCert),
				ClientKeyData:         "",
			},
			Service: v1alpha1.EnrollmentServiceService{
				CertificateAuthorityData: base64.StdEncoding.EncodeToString(caCert),
				Server:                   h.agentEndpoint,
			},
			EnrollmentUiEndpoint: h.uiUrl,
		},
	}, nil
}
