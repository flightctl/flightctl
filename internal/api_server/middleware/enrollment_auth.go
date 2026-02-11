package middleware

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	fcsigner "github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/jellydator/ttlcache/v3"
	"github.com/sirupsen/logrus"
)

// EnrollmentAuthMiddleware handles certificate-based authentication for enrollment/bootstrap requests
type EnrollmentAuthMiddleware struct {
	ca    *crypto.CAClient
	log   logrus.FieldLogger
	cache *ttlcache.Cache[string, *EnrollmentIdentity]
}

// NewEnrollmentAuthMiddleware creates a new enrollment authentication middleware
func NewEnrollmentAuthMiddleware(ca *crypto.CAClient, log logrus.FieldLogger) *EnrollmentAuthMiddleware {
	cache := ttlcache.New(
		ttlcache.WithTTL[string, *EnrollmentIdentity](10 * time.Minute),
	)

	return &EnrollmentAuthMiddleware{
		ca:    ca,
		log:   log,
		cache: cache,
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
			http.Error(w, "TLS connection required for enrollment authentication", http.StatusBadRequest)
			return
		}

		// Create cache key from certificate fingerprint
		cacheKey := m.createCacheKey(r.TLS)
		if cacheKey != "" {
			// Check cache first
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

		// Validate certificate signer using the exact same pattern as ValidateEnrollmentAccessFromContext
		signer := m.ca.PeerCertificateSignerFromCtx(ctx)

		got := "<nil>"
		if signer != nil {
			got = signer.Name()
		}

		if signer == nil || signer.Name() != m.ca.Cfg.DeviceEnrollmentSignerName {
			m.log.Warnf("unexpected client certificate signer: expected %q, got %q", m.ca.Cfg.DeviceEnrollmentSignerName, got)
			http.Error(w, fmt.Sprintf("unexpected client certificate signer: expected %q, got %q", m.ca.Cfg.DeviceEnrollmentSignerName, got), http.StatusUnauthorized)
			return
		}

		// Get peer certificate using the same pattern as handler.go
		peerCertificate, err := m.ca.PeerCertificateFromCtx(ctx)
		if err != nil {
			m.log.Warnf("Enrollment certificate validation failed: %v", err)
			http.Error(w, fmt.Sprintf("Certificate validation failed: %v", err), http.StatusUnauthorized)
			return
		}
		//get org ID from certificate extension
		orgID, present, err := fcsigner.GetOrgIDExtensionFromCert(peerCertificate)
		if err != nil {
			m.log.Errorf("Failed to extract organization ID from certificate: %v", err)
			http.Error(w, fmt.Sprintf("Failed to extract organization ID: %v", err), http.StatusUnauthorized)
			return
		}
		if !present {
			orgID = org.DefaultID
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
