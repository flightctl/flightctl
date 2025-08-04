package signer

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

// generateSelfSignedCertificate creates a minimal self-signed x509 certificate for testing purposes.
func generateSelfSignedCertificate() (*x509.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"flightctl"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	return x509.ParseCertificate(der)
}

// TestEncodeParseCertificatePEM ensures EncodeCertificatePEM and ParseCertificatePEM
// are exact inverses of each other.
func TestEncodeParseCertificatePEM(t *testing.T) {
	cert, err := generateSelfSignedCertificate()
	if err != nil {
		t.Fatalf("failed to generate test certificate: %v", err)
	}

	pemBytes, err := EncodeCertificatePEM(cert)
	if err != nil {
		t.Fatalf("EncodeCertificatePEM returned error: %v", err)
	}

	parsedCert, err := ParseCertificatePEM(pemBytes)
	if err != nil {
		t.Fatalf("ParseCertificatePEM returned error: %v", err)
	}

	if !bytes.Equal(cert.Raw, parsedCert.Raw) {
		t.Fatalf("original and parsed certificates differ")
	}
}
