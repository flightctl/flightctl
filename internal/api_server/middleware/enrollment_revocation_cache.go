package middleware

import (
	"context"
	"net/http"

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type revocationInvalidatingCSRService struct {
	csrservice.Service
	cache *RedisEnrollmentCSRStatusCache
	log   logrus.FieldLogger
}

// WrapCSRServiceWithRevocationCacheInvalidation removes the cached enrollment
// status for a credential after its CSR revocation is committed. Enrollment
// authentication may run in another process, so it observes the deletion
// through the shared key-value store.
func WrapCSRServiceWithRevocationCacheInvalidation(service csrservice.Service, cache *RedisEnrollmentCSRStatusCache, log logrus.FieldLogger) csrservice.Service {
	if service == nil || cache == nil {
		return service
	}
	return &revocationInvalidatingCSRService{Service: service, cache: cache, log: log}
}

func (s *revocationInvalidatingCSRService) RevokeCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, request domain.EnrollmentCertificateRevocationRequest) (*domain.CertificateSigningRequest, domain.Status) {
	result, status := s.Service.RevokeCertificateSigningRequest(ctx, orgId, name, request)
	if status.Code != http.StatusOK || result == nil || result.Status == nil || result.Status.Certificate == nil {
		return result, status
	}
	leaf, err := fcrypto.ParseCertificatePEM(*result.Status.Certificate)
	if err != nil {
		if s.log != nil {
			s.log.WithError(err).Warnf("Failed to parse revoked enrollment certificate for %q", name)
		}
		return result, status
	}
	if err := s.cache.Delete(ctx, orgId, crypto.CertificateDigest(leaf.Raw)); err != nil && s.log != nil {
		s.log.WithError(err).Warnf("Failed to invalidate cached enrollment status for %q", name)
	}
	return result, status
}
