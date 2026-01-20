package service

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/google/uuid"
)

func (h *ServiceHandler) GetEnrollmentConfig(ctx context.Context, orgId uuid.UUID, params domain.GetEnrollmentConfigParams) (*domain.EnrollmentConfig, domain.Status) {
	caCert, err := h.ca.GetCABundle()
	if err != nil {
		return nil, domain.StatusInternalServerError("failed to get CA certificate")
	}

	clientCert := []byte{}
	if params.Csr != nil {
		if errs := validation.ValidateResourceName(params.Csr); len(errs) > 0 {
			return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
		}

		csr, err := h.store.CertificateSigningRequest().Get(ctx, orgId, *params.Csr)
		if err != nil {
			return nil, StoreErrorToApiStatus(err, false, domain.CertificateSigningRequestKind, params.Csr)
		}

		if csr.Status != nil && csr.Status.Certificate != nil {
			clientCert = *csr.Status.Certificate
		}
	}

	return &domain.EnrollmentConfig{
		EnrollmentService: domain.EnrollmentService{
			Authentication: domain.EnrollmentServiceAuth{
				ClientCertificateData: base64.StdEncoding.EncodeToString(clientCert),
				ClientKeyData:         "",
			},
			Service: domain.EnrollmentServiceService{
				CertificateAuthorityData: base64.StdEncoding.EncodeToString(caCert),
				Server:                   h.agentEndpoint,
			},
			EnrollmentUiEndpoint: h.uiUrl,
		},
	}, domain.StatusOK()
}
