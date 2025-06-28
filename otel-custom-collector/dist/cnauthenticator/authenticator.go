package cnauthenticator

import (
	"context"
	"crypto/tls"
	"crypto/x509/pkix"
	"encoding/asn1"
	"log"
	"net/http"

	"go.opentelemetry.io/collector/component"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// OIDDeviceFingerprint defines the custom OID for device fingerprint
var OIDDeviceFingerprint = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 1}

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

// Authenticate extracts the client certificate CN and device fingerprint
func (c *cnAuthenticator) Authenticate(ctx context.Context, headers map[string][]string) (context.Context, error) {
	// Extract CN and device fingerprint from gRPC peer info
	if p, ok := peer.FromContext(ctx); ok {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			cn, deviceFingerprint := c.extractCertInfo(tlsInfo.State)
			if cn != "" {
				log.Printf("🎯 Client Certificate CN (gRPC): %s", cn)
			}
			if deviceFingerprint != "" {
				log.Printf("🔑 Device Fingerprint (gRPC): %s", deviceFingerprint)
			}
			// Add both CN and device fingerprint to context
			ctx = context.WithValue(ctx, "client_cert_cn", cn)
			ctx = context.WithValue(ctx, "device_fingerprint", deviceFingerprint)
		}
	}
	return ctx, nil
}

// AuthenticateHTTP extracts CN and device fingerprint from HTTP request
func (c *cnAuthenticator) AuthenticateHTTP(req *http.Request) (*http.Request, error) {
	if req.TLS != nil {
		cn, deviceFingerprint := c.extractCertInfo(*req.TLS)
		if cn != "" {
			log.Printf("🎯 Client Certificate CN (HTTP): %s", cn)
		}
		if deviceFingerprint != "" {
			log.Printf("🔑 Device Fingerprint (HTTP): %s", deviceFingerprint)
		}
		// Add both CN and device fingerprint to request context
		ctx := context.WithValue(req.Context(), "client_cert_cn", cn)
		ctx = context.WithValue(ctx, "device_fingerprint", deviceFingerprint)
		req = req.WithContext(ctx)
	}
	return req, nil
}

// extractCertInfo extracts both CN and device fingerprint from the client certificate
func (c *cnAuthenticator) extractCertInfo(state tls.ConnectionState) (string, string) {
	if len(state.PeerCertificates) == 0 {
		return "", ""
	}

	cert := state.PeerCertificates[0]
	cn := cert.Subject.CommonName

	// Extract device fingerprint from custom extension
	deviceFingerprint := c.extractDeviceFingerprint(cert.Extensions)

	return cn, deviceFingerprint
}

// extractDeviceFingerprint extracts the device fingerprint from certificate extensions
func (c *cnAuthenticator) extractDeviceFingerprint(extensions []pkix.Extension) string {
	for _, ext := range extensions {
		if ext.Id.Equal(OIDDeviceFingerprint) {
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
