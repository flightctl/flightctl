package middleware

import (
	"context"
	"testing"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type fakeRevokingCSRService struct {
	csrservice.Service
	result *domain.CertificateSigningRequest
	status domain.Status
	called bool
}

func (f *fakeRevokingCSRService) RevokeCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, request domain.EnrollmentCertificateRevocationRequest) (*domain.CertificateSigningRequest, domain.Status) {
	f.called = true
	return f.result, f.status
}

func TestRevocationCacheInvalidationWhenRevocationSucceedsItShouldDeleteTheCachedStatus(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	caClient, _, err := crypto.EnsureCA(cacfg.NewDefault(tempDir))
	require.NoError(t, err)
	certificate := namedEnrollmentCertificate(t, ctx, caClient, tempDir, "enrollment-credential")
	certificatePEM, err := fcrypto.EncodeCertificatePEM(certificate)
	require.NoError(t, err)

	cache, kv := newTestEnrollmentCSRStatusCache()
	orgId := uuid.New()
	digest := crypto.CertificateDigest(certificate.Raw)
	status := domain.CertificateSigningRequestStatus{}
	require.NoError(t, cache.Set(ctx, orgId, digest, enrollmentCSRStatusCacheEntry{Status: &status}))

	service := &fakeRevokingCSRService{
		result: &domain.CertificateSigningRequest{Status: &domain.CertificateSigningRequestStatus{Certificate: &certificatePEM}},
		status: domain.StatusOK(),
	}
	wrapped := WrapCSRServiceWithRevocationCacheInvalidation(service, cache, logrus.New())

	_, resultStatus := wrapped.RevokeCertificateSigningRequest(ctx, orgId, "enrollment-credential", domain.EnrollmentCertificateRevocationRequest{Reason: "Compromised"})

	require.Equal(t, domain.StatusOK(), resultStatus)
	require.True(t, service.called)
	_, found, err := cache.Get(ctx, orgId, digest)
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, 1, kv.deletes)
}

func TestRevocationCacheInvalidationWhenRevocationFailsItShouldKeepTheCachedStatus(t *testing.T) {
	cache, kv := newTestEnrollmentCSRStatusCache()
	service := &fakeRevokingCSRService{status: domain.StatusInternalServerError("revocation failed")}
	wrapped := WrapCSRServiceWithRevocationCacheInvalidation(service, cache, logrus.New())

	_, resultStatus := wrapped.RevokeCertificateSigningRequest(context.Background(), uuid.New(), "enrollment-credential", domain.EnrollmentCertificateRevocationRequest{Reason: "Compromised"})

	require.Equal(t, int32(500), resultStatus.Code)
	require.Zero(t, kv.deletes)
}

func TestRevocationCacheInvalidationWhenRevocationReturnsNoCertificateItShouldKeepTheCachedStatus(t *testing.T) {
	cache, kv := newTestEnrollmentCSRStatusCache()
	service := &fakeRevokingCSRService{status: domain.StatusOK(), result: &domain.CertificateSigningRequest{}}
	wrapped := WrapCSRServiceWithRevocationCacheInvalidation(service, cache, logrus.New())

	_, resultStatus := wrapped.RevokeCertificateSigningRequest(context.Background(), uuid.New(), "enrollment-credential", domain.EnrollmentCertificateRevocationRequest{Reason: "Compromised"})

	require.Equal(t, domain.StatusOK(), resultStatus)
	require.Zero(t, kv.deletes)
}
