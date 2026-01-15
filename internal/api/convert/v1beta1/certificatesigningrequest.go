package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// CertificateSigningRequestConverter converts between v1beta1 API types and domain types for CSR resources.
type CertificateSigningRequestConverter interface {
	ToDomain(apiv1beta1.CertificateSigningRequest) domain.CertificateSigningRequest
	FromDomain(*domain.CertificateSigningRequest) *apiv1beta1.CertificateSigningRequest
	ListFromDomain(*domain.CertificateSigningRequestList) *apiv1beta1.CertificateSigningRequestList

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListCertificateSigningRequestsParams) domain.ListCertificateSigningRequestsParams
}

type certificateSigningRequestConverter struct{}

// NewCertificateSigningRequestConverter creates a new CertificateSigningRequestConverter.
func NewCertificateSigningRequestConverter() CertificateSigningRequestConverter {
	return &certificateSigningRequestConverter{}
}

func (c *certificateSigningRequestConverter) ToDomain(csr apiv1beta1.CertificateSigningRequest) domain.CertificateSigningRequest {
	return csr
}

func (c *certificateSigningRequestConverter) FromDomain(csr *domain.CertificateSigningRequest) *apiv1beta1.CertificateSigningRequest {
	return csr
}

func (c *certificateSigningRequestConverter) ListFromDomain(l *domain.CertificateSigningRequestList) *apiv1beta1.CertificateSigningRequestList {
	return l
}

func (c *certificateSigningRequestConverter) ListParamsToDomain(p apiv1beta1.ListCertificateSigningRequestsParams) domain.ListCertificateSigningRequestsParams {
	return p
}
