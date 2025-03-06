package service

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/flightctl/flightctl/api/v1alpha1"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util/validation"
)

func (h *ServiceHandler) GetEnrollmentConfig(ctx context.Context, params api.GetEnrollmentConfigParams) (*api.EnrollmentConfig, api.Status) {
	orgId := store.NullOrgId

	caCert, _, err := h.ca.Config.GetPEMBytes()
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
	}, api.StatusOK()
}
