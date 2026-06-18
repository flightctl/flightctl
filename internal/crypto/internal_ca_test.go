package crypto

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"testing"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

func makeTestCSR(t *testing.T, cn string, dnsNames []string, ips []net.IP) *x509.CertificateRequest {
	t.Helper()
	_, priv, err := fccrypto.NewKeyPair()
	if err != nil {
		t.Fatalf("newKeyPair: %v", err)
	}
	tpl := &x509.CertificateRequest{
		Subject:     pkix.Name{CommonName: cn},
		DNSNames:    dnsNames,
		IPAddresses: ips,
	}
	raw, err := x509.CreateCertificateRequest(rand.Reader, tpl, priv.(crypto.Signer))
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csr, err := x509.ParseCertificateRequest(raw)
	if err != nil {
		t.Fatalf("parse csr: %v", err)
	}
	return csr
}

func TestIssueRequestedCertificateAsX509_ClientCertStripsSANs(t *testing.T) {
	cfg := cacfg.NewDefault(t.TempDir())
	caBackend, _, err := ensureInternalCA(cfg)
	if err != nil {
		t.Fatalf("ensureInternalCA: %v", err)
	}

	csr := makeTestCSR(t, "test-client", []string{"evil.example.com"}, []net.IP{net.ParseIP("10.0.0.1")})

	cert, err := caBackend.IssueRequestedCertificateAsX509(
		context.Background(),
		csr,
		86400,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	)
	if err != nil {
		t.Fatalf("IssueRequestedCertificateAsX509: %v", err)
	}

	if len(cert.DNSNames) > 0 {
		t.Errorf("client cert should not contain DNSNames, got %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) > 0 {
		t.Errorf("client cert should not contain IPAddresses, got %v", cert.IPAddresses)
	}
}

func TestIssueRequestedCertificateAsX509_ServerCertPreservesSANs(t *testing.T) {
	cfg := cacfg.NewDefault(t.TempDir())
	caBackend, _, err := ensureInternalCA(cfg)
	if err != nil {
		t.Fatalf("ensureInternalCA: %v", err)
	}

	dnsNames := []string{"api.example.com"}
	ips := []net.IP{net.ParseIP("10.0.0.1")}
	csr := makeTestCSR(t, "api.example.com", dnsNames, ips)

	cert, err := caBackend.IssueRequestedCertificateAsX509(
		context.Background(),
		csr,
		86400,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	)
	if err != nil {
		t.Fatalf("IssueRequestedCertificateAsX509: %v", err)
	}

	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "api.example.com" {
		t.Errorf("server cert should preserve DNSNames, got %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 1 {
		t.Errorf("server cert should preserve IPAddresses, got %v", cert.IPAddresses)
	}
}
