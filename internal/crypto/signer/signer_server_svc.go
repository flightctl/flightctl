package signer

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
)

const signerServerSvcExpiryDays int32 = 365

type SignerServerSvc struct {
	name string
	ca   CA
}

func NewSignerServerSvc(CAClient CA) Signer {
	cfg := CAClient.Config()
	return &SignerServerSvc{name: cfg.ServerSvcSignerName, ca: CAClient}
}

func (s *SignerServerSvc) Name() string {
	return s.name
}

func (s *SignerServerSvc) Verify(ctx context.Context, request SignRequest) error {
	if _, err := PeerCertificateFromCtx(ctx); err == nil {
		return fmt.Errorf("server csr is not allowed with peer certificate")
	}

	x509CSR := request.X509()
	if x509CSR.Subject.CommonName == "" {
		return fmt.Errorf("CSR CommonName cannot be empty for server certificates")
	}

	servicePrefix := "svc-"
	if !strings.HasPrefix(x509CSR.Subject.CommonName, servicePrefix) {
		return fmt.Errorf("CSR CommonName %q must start with prefix %q", x509CSR.Subject.CommonName, servicePrefix)
	}

	serviceName := strings.TrimPrefix(x509CSR.Subject.CommonName, servicePrefix)
	if serviceName == "" {
		return fmt.Errorf("CSR CommonName %q must include a service name after prefix %q", x509CSR.Subject.CommonName, servicePrefix)
	}

	return nil
}

func (s *SignerServerSvc) Sign(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
	if _, err := PeerCertificateFromCtx(ctx); err == nil {
		return nil, fmt.Errorf("server csr is not allowed with peer certificate")
	}

	x509CSR := request.X509()
	servicePrefix := "svc-"
	if !strings.HasPrefix(x509CSR.Subject.CommonName, servicePrefix) {
		return nil, fmt.Errorf("CSR CommonName %q must start with prefix %q", x509CSR.Subject.CommonName, servicePrefix)
	}

	serviceName := strings.TrimPrefix(x509CSR.Subject.CommonName, servicePrefix)
	if serviceName == "" {
		return nil, fmt.Errorf("CSR CommonName %q must include a service name after prefix %q", x509CSR.Subject.CommonName, servicePrefix)
	}

	expirySeconds := signerServerSvcExpiryDays * 24 * 60 * 60
	if request.ExpirationSeconds() != nil && *request.ExpirationSeconds() < expirySeconds {
		expirySeconds = *request.ExpirationSeconds()
	}

	return s.ca.IssueRequestedServerCertificate(
		ctx,
		&x509CSR,
		int(expirySeconds),
	)
}
