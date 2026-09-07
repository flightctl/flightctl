package enrollmentconfig

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/google/uuid"
)

type ServiceHandler struct {
	csrs          certificatesigningrequest.Service
	ca            *crypto.CAClient
	agentEndpoint string
	uiUrl         string
}

func NewServiceHandler(csrs certificatesigningrequest.Service, ca *crypto.CAClient, agentEndpoint string, uiUrl string) *ServiceHandler {
	return &ServiceHandler{csrs: csrs, ca: ca, agentEndpoint: agentEndpoint, uiUrl: uiUrl}
}

var _ Service = (*ServiceHandler)(nil)

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

		csr, status := h.csrs.GetCertificateSigningRequest(ctx, orgId, *params.Csr)
		if status.Code != http.StatusOK {
			return nil, status
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
