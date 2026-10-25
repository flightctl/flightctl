package middleware

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	fcsigner "github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/transport"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
	"github.com/sirupsen/logrus"
)

type EnrollmentCSRStatusCache interface {
	Get(ctx context.Context, orgId uuid.UUID, certificateDigest string) (domain.CertificateSigningRequestStatus, bool, error)
	Set(ctx context.Context, orgId uuid.UUID, certificateDigest string, status domain.CertificateSigningRequestStatus) error
}

// EnrollmentAuthMiddleware handles certificate-based authentication for enrollment/bootstrap requests
type EnrollmentAuthMiddleware struct {
	ca          *crypto.CAClient
	csrStatuses csrservice.Service
	statusCache EnrollmentCSRStatusCache
	log         logrus.FieldLogger
	cache       *ttlcache.Cache[string, *EnrollmentIdentity]
}

// NewEnrollmentAuthMiddleware creates a new enrollment authentication middleware
func NewEnrollmentAuthMiddleware(ca *crypto.CAClient, csrStatuses csrservice.Service, statusCache EnrollmentCSRStatusCache, log logrus.FieldLogger) *EnrollmentAuthMiddleware {
	cache := ttlcache.New(
		ttlcache.WithTTL[string, *EnrollmentIdentity](10 * time.Minute),
	)

	return &EnrollmentAuthMiddleware{
		ca:          ca,
		csrStatuses: csrStatuses,
		statusCache: statusCache,
		log:         log,
		cache:       cache,
	}
}

// Start starts the cache background cleanup
func (m *EnrollmentAuthMiddleware) Start() {
	m.cache.Start()
}

// Stop stops the cache background cleanup
func (m *EnrollmentAuthMiddleware) Stop() {
	m.cache.Stop()
}

// AuthenticateEnrollment is the middleware function that authenticates enrollment requests using certificates
func (m *EnrollmentAuthMiddleware) AuthenticateEnrollment(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Check if TLS connection exists
		if r.TLS == nil {
			m.log.Debug("No TLS connection for enrollment authentication")
			transport.WriteJSONError(w, r, "TLS connection required for enrollment authentication", http.StatusBadRequest)
			return
		}

		peerSigner := m.ca.PeerCertificateSignerFromCtx(ctx)
		if peerSigner == nil {
			m.log.Warn("could not determine peer certificate signer")
			transport.WriteJSONError(w, r, "could not determine peer certificate signer", http.StatusUnauthorized)
			return
		}

		if peerSigner.Name() != m.ca.Cfg.DeviceEnrollmentSignerName {
			m.log.Warnf("unexpected client certificate signer: expected %q, got %q", m.ca.Cfg.DeviceEnrollmentSignerName, peerSigner.Name())
			transport.WriteJSONError(w, r, fmt.Sprintf("unexpected client certificate signer: expected %q, got %q", m.ca.Cfg.DeviceEnrollmentSignerName, peerSigner.Name()), http.StatusUnauthorized)
			return
		}

		peerCertificate, err := m.ca.PeerCertificateFromCtx(ctx)
		if err != nil {
			m.log.Warnf("Enrollment certificate validation failed: %v", err)
			transport.WriteJSONError(w, r, fmt.Sprintf("Certificate validation failed: %v", err), http.StatusUnauthorized)
			return
		}

		orgID, present, err := fcsigner.GetOrgIDExtensionFromCert(peerCertificate)
		if err != nil {
			m.log.Errorf("Failed to extract organization ID from certificate: %v", err)
			transport.WriteJSONError(w, r, fmt.Sprintf("Failed to extract organization ID: %v", err), http.StatusUnauthorized)
			return
		}
		if !present {
			orgID = org.DefaultID
		}

		if !m.csrStatusAllowsEnrollment(ctx, orgID, peerCertificate) {
			transport.WriteJSONError(w, r, "Enrollment certificate is not approved", http.StatusUnauthorized)
			return
		}

		// Create cache key from certificate fingerprint
		cacheKey := m.createCacheKey(r.TLS)
		// Check the identity cache only after the CSR status decision.
		if cacheKey != "" {
			if item := m.cache.Get(cacheKey); item != nil {
				enrollmentIdentity := item.Value()
				// Validate that the cached identity hasn't expired
				if time.Now().Before(enrollmentIdentity.expirationDate) {
					ctx = context.WithValue(ctx, consts.IdentityCtxKey, enrollmentIdentity)
					m.log.Debugf("Enrollment authenticated from cache: cert=%s",
						enrollmentIdentity.commonName)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				} else {
					m.log.Debugf("Cached enrollment identity expired, re-validating certificate: cert=%s",
						enrollmentIdentity.commonName)
					// Remove expired entry from cache
					m.cache.Delete(cacheKey)
				}
			}
		}

		// Create enrollment identity with basic certificate information
		identity := &EnrollmentIdentity{
			orgID:          orgID.String(),
			commonName:     peerCertificate.Subject.CommonName,
			expirationDate: peerCertificate.NotAfter,
		}

		// Cache the identity if we have a valid cache key
		if cacheKey != "" {
			m.cache.Set(cacheKey, identity, ttlcache.DefaultTTL)
		}

		// Set identity in context for downstream middleware
		ctx = context.WithValue(ctx, consts.IdentityCtxKey, identity)

		// Log successful authentication
		m.log.Debugf("Enrollment authenticated: commonName=%s",
			identity.GetCommonName())

		// Continue to next middleware/handler
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *EnrollmentAuthMiddleware) csrStatusAllowsEnrollment(ctx context.Context, orgId uuid.UUID, peerCertificate *x509.Certificate) bool {
	name, err := fcrypto.GetCertificateExtensionValueAsStr(peerCertificate, fcsigner.OIDEnrollmentCSRName)
	if errors.Is(err, flterrors.ErrExtensionNotFound) {
		name = ""
	} else if err != nil {
		m.log.WithError(err).Warn("Failed to extract enrollment CSR name from certificate")
		return false
	}

	certificateDigest := crypto.CertificateDigest(peerCertificate.Raw)
	status, found, err := m.statusCache.Get(ctx, orgId, certificateDigest)
	if err != nil {
		m.log.WithError(err).Warnf("Failed to get cached CSR status for certificate digest %q", certificateDigest)
	} else if found {
		return domain.IsStatusConditionTrue(status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved)
	}

	csr, result := m.csrForCertificate(ctx, orgId, peerCertificate, name)
	if result.Code != http.StatusOK || csr == nil || csr.Status == nil {
		return false
	}
	if err := m.statusCache.Set(ctx, orgId, certificateDigest, *csr.Status); err != nil {
		m.log.WithError(err).Warnf("Failed to cache CSR status for certificate digest %q", certificateDigest)
	}
	return domain.IsStatusConditionTrue(csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved)
}

func (m *EnrollmentAuthMiddleware) csrForCertificate(ctx context.Context, orgId uuid.UUID, peerCertificate *x509.Certificate, name string) (*domain.CertificateSigningRequest, domain.Status) {
	if name != "" {
		return m.csrStatuses.GetCertificateSigningRequest(ctx, orgId, name)
	}

	certificateDigest := crypto.CertificateDigest(peerCertificate.Raw)
	filter := "internal.certificateDigest=" + certificateDigest
	var resolved *domain.CertificateSigningRequest
	var continuation *string
	for {
		candidates, status := m.csrStatuses.ListCertificateSigningRequests(
			ctx,
			orgId,
			domain.ListCertificateSigningRequestsParams{FieldSelector: &filter, Continue: continuation},
			selector.WithPrivateSelectors(),
		)
		if status.Code != http.StatusOK {
			return nil, status
		}
		for index, candidate := range candidates.Items {
			if candidate.Status == nil || candidate.Status.Certificate == nil {
				continue
			}
			issued, err := fcrypto.ParseCertificatePEM(*candidate.Status.Certificate)
			if err != nil || !bytes.Equal(issued.Raw, peerCertificate.Raw) {
				continue
			}
			if resolved != nil {
				return nil, domain.StatusInternalServerError("ambiguous enrollment credential certificate")
			}
			resolved = &candidates.Items[index]
		}
		continuation = candidates.Metadata.Continue
		if continuation == nil || *continuation == "" {
			return resolved, domain.StatusOK()
		}
	}
}

// createCacheKey creates a cache key from the TLS connection
// Uses the certificate fingerprint as the key for caching enrollment identities
func (m *EnrollmentAuthMiddleware) createCacheKey(tlsState *tls.ConnectionState) string {
	if len(tlsState.PeerCertificates) == 0 {
		return ""
	}

	// Use the certificate fingerprint as the cache key
	// This is unique per certificate and doesn't change during the certificate's lifetime
	cert := tlsState.PeerCertificates[0]
	return fmt.Sprintf("enrollment:%x", cert.Raw)
}

// CertificateInfo contains information extracted from the enrollment certificate
type CertificateInfo struct {
	CommonName     string
	ExpirationDate time.Time
}

// EnrollmentIdentity implements the common.Identity interface for enrollment requests
// This is different from device identities:
// - Uses certificate common name as identity (not device fingerprint)
// - No organization ID (enrollment is pre-organization)
// - No traditional roles (enrollment requests are authenticated by certificate)
// - Certificate-based issuer (not OIDC/AAP/K8s)
type EnrollmentIdentity struct {
	orgID          string
	commonName     string
	expirationDate time.Time
}

// GetUsername returns the certificate common name as the username
// For enrollment, this is the certificate identifier
func (e *EnrollmentIdentity) GetUsername() string {
	return e.commonName
}

// GetUID returns the certificate common name as the UID
// For enrollment, this is the certificate identifier
func (e *EnrollmentIdentity) GetUID() string {
	return e.commonName
}

// GetOrganizations returns the organization from the certificate
// Uses the orgID extracted from the certificate extension
func (e *EnrollmentIdentity) GetOrganizations() []common.ReportedOrganization {
	orgID := e.orgID
	if orgID == "" {
		orgID = org.DefaultID.String()
	}
	return []common.ReportedOrganization{
		{
			Name:         orgID,
			IsInternalID: true,
			ID:           orgID,
		},
	}
}

// GetIssuer returns the certificate issuer
// Enrollment uses certificate-based authentication
func (e *EnrollmentIdentity) GetIssuer() *identity.Issuer {
	return &identity.Issuer{
		Type: "certificate",
		ID:   "enrollment-cert",
	}
}

// GetOrgID returns the organization ID from the certificate
// Uses the orgID extracted from the certificate extension
func (e *EnrollmentIdentity) GetOrgID() string {
	return e.orgID
}

// GetCommonName returns the certificate common name
func (e *EnrollmentIdentity) GetCommonName() string {
	return e.commonName
}

// IsAgent returns false to identify this as an enrollment identity (not an agent)
func (e *EnrollmentIdentity) IsAgent() bool {
	return false
}

// GetExpirationDate returns the certificate expiration date
func (e *EnrollmentIdentity) GetExpirationDate() time.Time {
	return e.expirationDate
}

// IsSuperAdmin returns false for enrollment identities (enrollment has no super admin concept)
func (e *EnrollmentIdentity) IsSuperAdmin() bool {
	return false
}

// SetSuperAdmin is a no-op for enrollment identities (enrollment has no super admin concept)
func (e *EnrollmentIdentity) SetSuperAdmin(superAdmin bool) {
	// No-op: enrollment identities don't support super admin
}
