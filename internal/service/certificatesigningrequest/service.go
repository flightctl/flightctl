package certificatesigningrequest

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	FindNameByCertificate(ctx context.Context, orgId uuid.UUID, leafDER []byte) (*string, error)
	ListCertificateSigningRequests(ctx context.Context, orgId uuid.UUID, params domain.ListCertificateSigningRequestsParams) (*domain.CertificateSigningRequestList, domain.Status)
	CreateCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status)
	DeleteCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) domain.Status
	GetCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, domain.Status)
	PatchCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.CertificateSigningRequest, domain.Status)
	ReplaceCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status)
	UpdateCertificateSigningRequestApproval(ctx context.Context, orgId uuid.UUID, name string, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status)
}
