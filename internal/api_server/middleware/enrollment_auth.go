package middleware

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	fcsigner "github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/transport"
	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
	"github.com/sirupsen/logrus"
)

// EnrollmentAuthMiddleware handles certificate-based authentication for enrollment/bootstrap requests
type EnrollmentAuthMiddleware struct {
	ca          *crypto.CAClient
	csrStatuses csrservice.Service
	statusCache *RedisEnrollmentCSRStatusCache
	log         logrus.FieldLogger
	cache       *ttlcache.Cache[string, *EnrollmentIdentity]
	mu          sync.Mutex
	cancel      context.CancelFunc
	started     bool
	stopOnce    sync.Once
}

// NewEnrollmentAuthMiddleware creates a new enrollment authentication middleware
func NewEnrollmentAuthMiddleware(ca *crypto.CAClient, csrStatuses csrservice.Service, statusCache *RedisEnrollmentCSRStatusCache, log logrus.FieldLogger) (*EnrollmentAuthMiddleware, error) {
	if ca == nil {
		return nil, errors.New("ca is required")
	}
	if csrStatuses == nil {
		return nil, errors.New("csrStatuses is required")
	}
	if statusCache == nil {
		return nil, errors.New("statusCache is required")
	}
	if log == nil {
		return nil, errors.New("log is required")
	}
	cache := ttlcache.New(
		ttlcache.WithTTL[string, *EnrollmentIdentity](10 * time.Minute),
	)

	return &EnrollmentAuthMiddleware{
		ca:          ca,
		csrStatuses: csrStatuses,
		statusCache: statusCache,
		log:         log,
		cache:       cache,
	}, nil
}

// Start starts the cache background cleanup. It creates a child context that
// can be independently canceled via Stop.
func (m *EnrollmentAuthMiddleware) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("enrollment auth middleware already started")
	}

	middlewareCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.started = true

	go m.cache.Start()
	go func() {
		<-middlewareCtx.Done()
		m.cache.Stop()
		m.log.Debug("Enrollment auth identity cache stopped")
	}()

	return nil
}

// Stop stops the cache background cleanup.
func (m *EnrollmentAuthMiddleware) Stop() {
	m.mu.Lock()
	started := m.started
	cancel := m.cancel
	m.mu.Unlock()

	if !started {
		return
	}
	m.stopOnce.Do(func() {
		if cancel != nil {
			cancel()
		}
	})
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

		credentialName, allowed := m.CSRStatusAllowsEnrollment(ctx, orgID, peerCertificate)
		if !allowed {
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
			orgID:             orgID.String(),
			commonName:        peerCertificate.Subject.CommonName,
			expirationDate:    peerCertificate.NotAfter,
			enrollmentCSRName: credentialName,
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

func (m *EnrollmentAuthMiddleware) CSRStatusAllowsEnrollment(ctx context.Context, orgId uuid.UUID, peerCertificate *x509.Certificate) (*string, bool) {
	certificateDigest := crypto.CertificateDigest(peerCertificate.Raw)
	entry, found, err := m.statusCache.Get(ctx, orgId, certificateDigest)
	if err != nil {
		m.log.WithError(err).Warnf("Failed to get cached CSR status for certificate digest %q", certificateDigest)
	} else if found {
		if entry.Status == nil {
			return nil, false
		}
		if !domain.IsStatusConditionTrue(entry.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved) {
			return entry.Name, false
		}
	}

	csr, err := csrservice.CSRForLeaf(ctx, m.csrStatuses, orgId, peerCertificate)
	if errors.Is(err, csrservice.ErrEnrollmentCSRNotFound) {
		if cacheErr := m.statusCache.SetMissing(ctx, orgId, certificateDigest); cacheErr != nil {
			m.log.WithError(cacheErr).Warnf("Failed to cache missing CSR status for certificate digest %q", certificateDigest)
		}
		return nil, false
	}
	if err != nil {
		m.log.WithError(err).Warn("Failed to resolve enrollment certificate to CSR")
		return nil, false
	}
	if csr.Status == nil {
		if cacheErr := m.statusCache.SetMissing(ctx, orgId, certificateDigest); cacheErr != nil {
			m.log.WithError(cacheErr).Warnf("Failed to cache missing CSR status for certificate digest %q", certificateDigest)
		}
		return nil, false
	}
	approved := domain.IsStatusConditionTrue(csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved)
	if !approved {
		if cacheErr := m.statusCache.Set(ctx, orgId, certificateDigest, enrollmentCSRStatusCacheEntry{Name: csr.Metadata.Name, Status: csr.Status}); cacheErr != nil {
			m.log.WithError(cacheErr).Warnf("Failed to cache CSR status for certificate digest %q", certificateDigest)
		}
	}
	return csr.Metadata.Name, approved
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
	orgID             string
	commonName        string
	expirationDate    time.Time
	enrollmentCSRName *string
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

// GetEnrollmentCSRName returns the CSR resolved for the presented enrollment leaf.
func (e *EnrollmentIdentity) GetEnrollmentCSRName() *string {
	return e.enrollmentCSRName
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
