package signer

import (
	"context"
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

func (s *SignerServerSvc) Verify(ctx context.Context, request *Request) error {
	// For server service certificates, we require CLI authentication
	// Users must authenticate through the CLI (same as other operations)

	if _, err := PeerCertificateFromCtx(ctx); err == nil {
		return fmt.Errorf("server csr is not allowed with peer certificate")
	}

	// For server certificates, we expect the CN to be a service name with svc- prefix
	x509CSR := request.X509()
	if x509CSR.Subject.CommonName == "" {
		return fmt.Errorf("CSR CommonName cannot be empty for server certificates")
	}

	// Validate that the CN starts with the expected service prefix
	servicePrefix := "svc-"
	if !strings.HasPrefix(x509CSR.Subject.CommonName, servicePrefix) {
		return fmt.Errorf("CSR CommonName %q must start with prefix %q", x509CSR.Subject.CommonName, servicePrefix)
	}

	// Extract service name and validate it's not empty
	serviceName := strings.TrimPrefix(x509CSR.Subject.CommonName, servicePrefix)
	if serviceName == "" {
		return fmt.Errorf("CSR CommonName %q must include a service name after prefix %q", x509CSR.Subject.CommonName, servicePrefix)
	}

	return nil
}

func (s *SignerServerSvc) Sign(ctx context.Context, request *Request) ([]byte, error) {

	if _, err := PeerCertificateFromCtx(ctx); err == nil {
		return nil, fmt.Errorf("server csr is not allowed with peer certificate")
	}

	// Ensure the CommonName follows the service naming convention
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
	if request.API.Spec.ExpirationSeconds != nil && *request.API.Spec.ExpirationSeconds < expirySeconds {
		expirySeconds = *request.API.Spec.ExpirationSeconds
	}

	return s.ca.IssueRequestedServerCertificate(
		ctx,
		&x509CSR,
		int(expirySeconds),
	)
}
