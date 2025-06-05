package service

import (
	"context"
	"encoding/base64"
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util/validation"
)

func (h *ServiceHandler) GetEnrollmentConfig(ctx context.Context, params api.GetEnrollmentConfigParams) (*api.EnrollmentConfig, api.Status) {
	orgId := store.NullOrgId

	caCert, err := h.ca.GetCABundle()
	if err != nil {
		return nil, api.StatusInternalServerError("failed to get CA certificate")
	}

	clientCert := []byte{}
	if params.Csr != nil {
		if errs := validation.ValidateResourceName(params.Csr); len(errs) > 0 {
			return nil, api.StatusBadRequest(errors.Join(errs...).Error())
		}

		csr, err := h.store.CertificateSigningRequest().Get(ctx, orgId, *params.Csr)
		if err != nil {
			return nil, StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, params.Csr)
		}

		if csr.Status != nil && csr.Status.Certificate != nil {
			clientCert = *csr.Status.Certificate
		}
	}

	return &api.EnrollmentConfig{
		EnrollmentService: api.EnrollmentService{
			Authentication: api.EnrollmentServiceAuth{
				ClientCertificateData: base64.StdEncoding.EncodeToString(clientCert),
				ClientKeyData:         "",
			},
			Service: api.EnrollmentServiceService{
				CertificateAuthorityData: base64.StdEncoding.EncodeToString(caCert),
				Server:                   h.agentEndpoint,
			},
			EnrollmentUiEndpoint: h.uiUrl,
		},
	}, api.StatusOK()
}
