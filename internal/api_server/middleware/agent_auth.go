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
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/jellydator/ttlcache/v3"
	"github.com/sirupsen/logrus"
)

// AgentAuthMiddleware handles certificate-based authentication for device agents
// This middleware is specifically for device operations that use DeviceManagementSignerName
type AgentAuthMiddleware struct {
	ca    *crypto.CAClient
	log   logrus.FieldLogger
	cache *ttlcache.Cache[string, *AgentIdentity]
}

// NewAgentAuthMiddleware creates a new device agent authentication middleware
func NewAgentAuthMiddleware(ca *crypto.CAClient, log logrus.FieldLogger) *AgentAuthMiddleware {
	cache := ttlcache.New[string, *AgentIdentity](
		ttlcache.WithTTL[string, *AgentIdentity](10 * time.Minute),
	)

	return &AgentAuthMiddleware{
		ca:    ca,
		log:   log,
		cache: cache,
	}
}

// Start starts the cache background cleanup
func (m *AgentAuthMiddleware) Start() {
	m.cache.Start()
}

// Stop stops the cache background cleanup
func (m *AgentAuthMiddleware) Stop() {
	m.cache.Stop()
}

// AuthenticateAgent is the middleware function that authenticates agents using certificates
func (m *AgentAuthMiddleware) AuthenticateAgent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Check if TLS connection exists
		if r.TLS == nil {
			m.log.Debug("No TLS connection for agent authentication")
			http.Error(w, "TLS connection required for agent authentication", http.StatusBadRequest)
			return
		}

		// Create cache key from certificate fingerprint
		cacheKey := m.createCacheKey(r.TLS)
		if cacheKey != "" {
			// Check cache first
			if item := m.cache.Get(cacheKey); item != nil {
				agentIdentity := item.Value()
				// Validate that the cached identity hasn't expired
				if time.Now().Before(agentIdentity.expirationDate) {
					ctx = context.WithValue(ctx, consts.IdentityCtxKey, agentIdentity)
					m.log.Debugf("Agent authenticated from cache: device=%s, org=%s",
						agentIdentity.GetUsername(), agentIdentity.GetOrgID())
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				} else {
					m.log.Debugf("Cached agent identity expired, re-validating certificate: device=%s",
						agentIdentity.deviceFingerprint)
					// Remove expired entry from cache
					m.cache.Delete(cacheKey)
				}
			}
		}

		// Validate certificate signer using the same pattern as handler.go
		if s := m.ca.PeerCertificateSignerFromCtx(ctx); s != nil && s.Name() != m.ca.Cfg.DeviceManagementSignerName {
			m.log.Warnf("unexpected client certificate signer: expected %q, got %q", m.ca.Cfg.DeviceManagementSignerName, s.Name())
			http.Error(w, fmt.Sprintf("unexpected client certificate signer: expected %q, got %q", m.ca.Cfg.DeviceManagementSignerName, s.Name()), http.StatusUnauthorized)
			return
		}

		// Get peer certificate using the same pattern as handler.go
		peerCertificate, err := m.ca.PeerCertificateFromCtx(ctx)
		if err != nil {
			m.log.Warnf("Agent certificate validation failed: %v", err)
			http.Error(w, fmt.Sprintf("Certificate validation failed: %v", err), http.StatusUnauthorized)
			return
		}

		// Extract device fingerprint using the same pattern as handler.go
		fingerprint, err := signer.DeviceFingerprintFromCN(m.ca.Cfg, peerCertificate.Subject.CommonName)
		if err != nil {
			m.log.Errorf("Failed to extract device fingerprint: %v", err)
			http.Error(w, fmt.Sprintf("Failed to extract device fingerprint: %v", err), http.StatusUnauthorized)
			return
		}

		// Extract organization ID from certificate extension
		orgID, present, err := signer.GetOrgIDExtensionFromCert(peerCertificate)
		if err != nil {
			m.log.Errorf("Failed to extract organization ID from certificate: %v", err)
			http.Error(w, fmt.Sprintf("Failed to extract organization ID: %v", err), http.StatusUnauthorized)
			return
		}

		// Use default org ID if not present in certificate
		if !present {
			orgID = org.DefaultID
		}

		// Create agent identity with the extracted information
		identity := &AgentIdentity{
			deviceFingerprint: fingerprint,
			orgID:             orgID.String(),
			commonName:        peerCertificate.Subject.CommonName,
			expirationDate:    peerCertificate.NotAfter,
		}

		// Cache the identity if we have a valid cache key
		if cacheKey != "" {
			m.cache.Set(cacheKey, identity, ttlcache.DefaultTTL)
		}

		// Set identity in context for downstream middleware
		ctx = context.WithValue(ctx, consts.IdentityCtxKey, identity)

		// Log successful authentication
		m.log.Debugf("Agent authenticated: device=%s",
			identity.GetUsername())

		// Continue to next middleware/handler
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// createCacheKey creates a cache key from the TLS connection
// Uses the certificate fingerprint as the key for caching agent identities
func (m *AgentAuthMiddleware) createCacheKey(tlsState *tls.ConnectionState) string {
	if len(tlsState.PeerCertificates) == 0 {
		return ""
	}

	// Use the certificate fingerprint as the cache key
	// This is unique per certificate and doesn't change during the certificate's lifetime
	cert := tlsState.PeerCertificates[0]
	return fmt.Sprintf("agent:%x", cert.Raw)
}

// DeviceInfo contains information extracted from the agent certificate
type DeviceInfo struct {
	DeviceFingerprint string
	OrgID             string
	CommonName        string
	ExpirationDate    time.Time
}

// AgentIdentity implements the common.Identity interface for agents
// This is fundamentally different from user identities:
// - Uses device fingerprint as identity (not human username)
// - Has direct database organization ID (not external org names)
// - No traditional roles (agents are authenticated by certificate)
// - Certificate-based issuer (not OIDC/AAP/K8s)
type AgentIdentity struct {
	deviceFingerprint string
	orgID             string
	commonName        string
	expirationDate    time.Time
}

// GetUsername returns the device fingerprint as the username
// For agents, this is the device identifier, not a human username
func (a *AgentIdentity) GetUsername() string {
	return a.deviceFingerprint
}

// GetUID returns the device fingerprint as the UID
// For agents, this is the device identifier, not a human user ID
func (a *AgentIdentity) GetUID() string {
	return a.deviceFingerprint
}

// GetOrganizations returns the organization ID as a single organization
// For agents, this is the actual database organization ID (UUID)
// Unlike users who have external org names that get mapped to DB orgs
func (a *AgentIdentity) GetOrganizations() []common.ReportedOrganization {
	return []common.ReportedOrganization{
		{
			Name:         a.orgID,
			IsInternalID: true,
			ID:           a.orgID,
		},
	}
}

// GetIssuer returns the certificate issuer
// Agents use certificate-based authentication, not OIDC/AAP/K8s
func (a *AgentIdentity) GetIssuer() *identity.Issuer {
	return &identity.Issuer{
		Type: "certificate",
		ID:   "agent-cert",
	}
}

// GetOrgID returns the organization ID from the certificate
// This is the actual database organization ID (UUID)
func (a *AgentIdentity) GetOrgID() string {
	return a.orgID
}

// GetCommonName returns the certificate common name
func (a *AgentIdentity) GetCommonName() string {
	return a.commonName
}

// IsAgent returns true to identify this as an agent identity
func (a *AgentIdentity) IsAgent() bool {
	return true
}

// GetExpirationDate returns the certificate expiration date
func (a *AgentIdentity) GetExpirationDate() time.Time {
	return a.expirationDate
}

// IsSuperAdmin returns false for agent identities (agents have no super admin concept)
func (a *AgentIdentity) IsSuperAdmin() bool {
	return false
}

// SetSuperAdmin is a no-op for agent identities (agents have no super admin concept)
func (a *AgentIdentity) SetSuperAdmin(superAdmin bool) {
	// No-op: agent identities don't support super admin
}
