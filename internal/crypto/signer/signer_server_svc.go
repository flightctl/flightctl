package signer

import (
	"context"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
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

func (s *SignerServerSvc) Verify(ctx context.Context, request api.CertificateSigningRequest) error {
	// For server service certificates, we require CLI authentication
	// Users must authenticate through the CLI (same as other operations)

	if _, err := PeerCertificateFromCtx(ctx); err == nil {
		return fmt.Errorf("server csr is not allowed with peer certificate")
	}

	parsedCSR, err := fccrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return fmt.Errorf("failed to parse CSR: %w", err)
	}

	// For server certificates, we expect the CN to be a service name with svc- prefix
	if parsedCSR.Subject.CommonName == "" {
		return fmt.Errorf("CSR CommonName cannot be empty for server certificates")
	}

	// Validate that the CN starts with the expected service prefix
	servicePrefix := "svc-"
	if !strings.HasPrefix(parsedCSR.Subject.CommonName, servicePrefix) {
		return fmt.Errorf("CSR CommonName %q must start with prefix %q", parsedCSR.Subject.CommonName, servicePrefix)
	}

	// Extract service name and validate it's not empty
	serviceName := strings.TrimPrefix(parsedCSR.Subject.CommonName, servicePrefix)
	if serviceName == "" {
		return fmt.Errorf("CSR CommonName %q must include a service name after prefix %q", parsedCSR.Subject.CommonName, servicePrefix)
	}

	return nil
}

func (s *SignerServerSvc) Sign(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {

	if _, err := PeerCertificateFromCtx(ctx); err == nil {
		return nil, fmt.Errorf("server csr is not allowed with peer certificate")
	}

	cert, err := fccrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, err
	}

	// Ensure the CommonName follows the service naming convention
	servicePrefix := "svc-"
	if !strings.HasPrefix(cert.Subject.CommonName, servicePrefix) {
		return nil, fmt.Errorf("CSR CommonName %q must start with prefix %q", cert.Subject.CommonName, servicePrefix)
	}

	serviceName := strings.TrimPrefix(cert.Subject.CommonName, servicePrefix)
	if serviceName == "" {
		return nil, fmt.Errorf("CSR CommonName %q must include a service name after prefix %q", cert.Subject.CommonName, servicePrefix)
	}

	expirySeconds := signerServerSvcExpiryDays * 24 * 60 * 60
	if request.Spec.ExpirationSeconds != nil && *request.Spec.ExpirationSeconds < expirySeconds {
		expirySeconds = *request.Spec.ExpirationSeconds
	}

	return s.ca.IssueRequestedServerCertificate(
		ctx,
		cert,
		int(expirySeconds),
	)
}
