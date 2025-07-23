package cnauthenticator

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"log"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/collector/component"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// ContextKey is a custom type for context keys to avoid collisions
type ContextKey string

const (
	ClientCertCNKey ContextKey = "client_cert_cn"
	DeviceIDKey     ContextKey = "device_id"
	OrgIDKey        ContextKey = "org_id"
)

// OIDSignerName defines the custom OID for signer name
var OIDSignerName = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 1}

// OIDOrgID defines the custom OID for organization ID
var OIDOrgID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 2}

// ExpectedDeviceSvcClientSignerName is the expected signer name for device service client certificates
const ExpectedDeviceSvcClientSignerName = "flightctl.io/device-svc-client"

// CertInfo contains the extracted information from a client certificate
type CertInfo struct {
	CommonName string
	DeviceID   string
	OrgID      string
}

// cnAuthenticator implements the auth.Server interface for CN-based authentication
type cnAuthenticator struct {
}

// newCNAuthenticator creates a new CN authenticator
func newCNAuthenticator() *cnAuthenticator {
	return &cnAuthenticator{}
}

// Start does nothing as this authenticator doesn't need startup logic
func (c *cnAuthenticator) Start(ctx context.Context, host component.Host) error {
	log.Println("CN Authenticator with Device Fingerprint started")
	return nil
}

// Shutdown does nothing as this authenticator doesn't need cleanup
func (c *cnAuthenticator) Shutdown(ctx context.Context) error {
	log.Println("CN Authenticator stopped")
	return nil
}

// Authenticate extracts the client certificate CN and device ID
func (c *cnAuthenticator) Authenticate(ctx context.Context, headers map[string][]string) (context.Context, error) {
	// Extract CN and device ID from gRPC peer info
	if p, ok := peer.FromContext(ctx); ok {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			certInfo := c.extractCertInfo(tlsInfo.State)
			if certInfo.CommonName != "" {
				log.Printf("🎯 Client Certificate CN (gRPC): %s", certInfo.CommonName)
			}
			if certInfo.DeviceID != "" {
				log.Printf("🔑 Device ID (gRPC): %s", certInfo.DeviceID)
			}
			if certInfo.OrgID != "" {
				log.Printf("🏢 Org ID (gRPC): %s", certInfo.OrgID)
			}
			// Add both CN and device ID to context
			ctx = context.WithValue(ctx, ClientCertCNKey, certInfo.CommonName)
			ctx = context.WithValue(ctx, DeviceIDKey, certInfo.DeviceID)
			ctx = context.WithValue(ctx, OrgIDKey, certInfo.OrgID)
		}
	}
	return ctx, nil
}

// AuthenticateHTTP extracts CN and device ID from HTTP request
func (c *cnAuthenticator) AuthenticateHTTP(req *http.Request) (*http.Request, error) {
	if req.TLS != nil {
		certInfo := c.extractCertInfo(*req.TLS)
		if certInfo.CommonName != "" {
			log.Printf("🎯 Client Certificate CN (HTTP): %s", certInfo.CommonName)
		}
		if certInfo.DeviceID != "" {
			log.Printf("🔑 Device ID (HTTP): %s", certInfo.DeviceID)
		}
		if certInfo.OrgID != "" {
			log.Printf("🏢 Org ID (HTTP): %s", certInfo.OrgID)
		}
		// Add both CN and device ID to request context
		ctx := context.WithValue(req.Context(), ClientCertCNKey, certInfo.CommonName)
		ctx = context.WithValue(ctx, DeviceIDKey, certInfo.DeviceID)
		ctx = context.WithValue(ctx, OrgIDKey, certInfo.OrgID)
		req = req.WithContext(ctx)
	}
	return req, nil
}

// extractCertInfo extracts both CN and device ID from the client certificate
func (c *cnAuthenticator) extractCertInfo(state tls.ConnectionState) CertInfo {
	if len(state.PeerCertificates) == 0 {
		return CertInfo{}
	}

	// Iterate through all peer certificates to find the one signed by the device service client signer
	for _, cert := range state.PeerCertificates {
		cn := cert.Subject.CommonName

		// Check if this certificate was signed by the device service client signer
		signerName := c.extractSignerName(cert.Extensions)
		if signerName == ExpectedDeviceSvcClientSignerName {
			log.Printf("✅ Found certificate signed by device service client signer: %s", cn)

			// Additional security validations
			if !c.validateCertificate(cert) {
				log.Printf("❌ Certificate validation failed for: %s", cn)
				continue // Try next certificate
			}

			// Extract device ID from CN by taking everything after the last hyphen
			deviceID := c.extractDeviceIDFromCN(cn)

			// Verify device ID matches the fingerprint in certificate extension
			if !c.verifyDeviceFingerprint(cert, deviceID) {
				log.Printf("❌ Device fingerprint verification failed for: %s", cn)
				continue // Try next certificate
			}

			// Extract Org ID from certificate extension
			orgID := c.extractOrgID(cert.Extensions)
			if orgID == "" {
				// Fallback: extract Org ID from CN by taking everything before the last hyphen
				orgID = c.extractOrgIDFromCN(cn)
			}

			return CertInfo{
				CommonName: cn,
				DeviceID:   deviceID,
				OrgID:      orgID,
			}
		}
	}

	// If no certificate with the expected signer was found, log and return empty device ID
	log.Printf("❌ No certificate found with expected signer %q", ExpectedDeviceSvcClientSignerName)
	return CertInfo{}
}

// validateCertificate performs comprehensive certificate validation
func (c *cnAuthenticator) validateCertificate(cert *x509.Certificate) bool {
	// Check certificate expiry
	if cert.NotAfter.Before(time.Now()) {
		log.Printf("❌ Certificate expired: %s", cert.NotAfter)
		return false
	}

	if cert.NotBefore.After(time.Now()) {
		log.Printf("❌ Certificate not yet valid: %s", cert.NotBefore)
		return false
	}

	// Check key usage - should be for client authentication
	if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		log.Printf("❌ Certificate missing digital signature key usage")
		return false
	}

	// Check extended key usage - should include client authentication
	hasClientAuth := false
	for _, usage := range cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
			break
		}
	}
	if !hasClientAuth {
		log.Printf("❌ Certificate missing client authentication extended key usage")
		return false
	}

	return true
}

// verifyDeviceFingerprint verifies that the device ID matches the fingerprint in certificate extension
func (c *cnAuthenticator) verifyDeviceFingerprint(cert *x509.Certificate, deviceID string) bool {
	// Extract device fingerprint from certificate extension
	deviceFingerprint := c.extractDeviceFingerprint(cert.Extensions)
	if deviceFingerprint == "" {
		log.Printf("❌ No device fingerprint found in certificate extension")
		return false
	}

	// Verify device ID matches the fingerprint
	if deviceID != deviceFingerprint {
		log.Printf("❌ Device ID mismatch: CN-derived=%s, extension=%s", deviceID, deviceFingerprint)
		return false
	}

	return true
}

// extractDeviceFingerprint extracts the device fingerprint from certificate extensions
func (c *cnAuthenticator) extractDeviceFingerprint(extensions []pkix.Extension) string {
	// OID for device fingerprint extension
	oidDeviceFingerprint := asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 3}

	for _, ext := range extensions {
		if ext.Id.Equal(oidDeviceFingerprint) {
			// The extension value is ASN.1 encoded, try to decode it
			var fingerprint string
			if _, err := asn1.Unmarshal(ext.Value, &fingerprint); err == nil {
				return fingerprint
			}

			// If direct string unmarshal fails, try as raw bytes (might be UTF8 string)
			if len(ext.Value) > 0 {
				// Skip ASN.1 tag and length if present
				value := ext.Value
				if len(value) > 2 && value[0] == 0x0C { // UTF8String tag
					length := int(value[1])
					if length > 0 && length <= len(value)-2 {
						return string(value[2 : 2+length])
					}
				}
				// Fallback: return raw bytes as string
				return string(ext.Value)
			}
		}
	}
	return ""
}

// extractSignerName extracts the signer name from certificate extensions
func (c *cnAuthenticator) extractSignerName(extensions []pkix.Extension) string {
	for _, ext := range extensions {
		if ext.Id.Equal(OIDSignerName) {
			// The extension value is ASN.1 encoded, try to decode it
			var signerName string
			if _, err := asn1.Unmarshal(ext.Value, &signerName); err == nil {
				return signerName
			}

			// If direct string unmarshal fails, try as raw bytes (might be UTF8 string)
			if len(ext.Value) > 0 {
				// Skip ASN.1 tag and length if present
				value := ext.Value
				if len(value) > 2 && value[0] == 0x0C { // UTF8String tag
					length := int(value[1])
					if length > 0 && length <= len(value)-2 {
						return string(value[2 : 2+length])
					}
				}
				// Fallback: return raw bytes as string
				return string(ext.Value)
			}
		}
	}
	return ""
}

// extractOrgID extracts the organization ID from certificate extensions
func (c *cnAuthenticator) extractOrgID(extensions []pkix.Extension) string {
	// OID for organization ID extension
	oidOrgID := asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 2}

	for _, ext := range extensions {
		if ext.Id.Equal(oidOrgID) {
			// The extension value is ASN.1 encoded, try to decode it
			var orgID string
			if _, err := asn1.Unmarshal(ext.Value, &orgID); err == nil {
				return orgID
			}

			// If direct string unmarshal fails, try as raw bytes (might be UTF8 string)
			if len(ext.Value) > 0 {
				// Skip ASN.1 tag and length if present
				value := ext.Value
				if len(value) > 2 && value[0] == 0x0C { // UTF8String tag
					length := int(value[1])
					if length > 0 && length <= len(value)-2 {
						return string(value[2 : 2+length])
					}
				}
				// Fallback: return raw bytes as string
				return string(ext.Value)
			}
		}
	}
	return ""
}

// extractDeviceIDFromCN extracts the device ID from the certificate CN
// This follows the same logic as signer_device_svc_client.go
func (c *cnAuthenticator) extractDeviceIDFromCN(cn string) string {
	lastHyphen := strings.LastIndex(cn, "-")
	if lastHyphen == -1 {
		// If no hyphen found, return the full CN
		return cn
	}
	return cn[lastHyphen+1:]
}

// extractOrgIDFromCN extracts the organization ID from the certificate CN
// This follows the same logic as signer_device_svc_client.go
func (c *cnAuthenticator) extractOrgIDFromCN(cn string) string {
	lastHyphen := strings.LastIndex(cn, "-")
	if lastHyphen == -1 {
		// If no hyphen found, return the full CN
		return cn
	}
	return cn[:lastHyphen]
}
