package signer

import (
	"context"
	"crypto/x509"
	"fmt"

	"github.com/flightctl/flightctl/internal/flterrors"
)

const DefaultEnrollmentCertExpirySeconds int32 = 60 * 60 * 24 * 7 // 7 days

type SignerClientBootstrap struct {
	name string
	ca   CA
}

func NewClientBootstrap(CAClient CA) Signer {
	cfg := CAClient.Config()
	return &SignerClientBootstrap{name: cfg.ClientBootstrapSignerName, ca: CAClient}
}

func (s *SignerClientBootstrap) Name() string {
	return s.name
}

func (s *SignerClientBootstrap) Verify(ctx context.Context, request SignRequest) error {
	// We are about to expose CreateCertificateSigningRequest to agents.
	// Currently, there is no code in the agent that handles this flow for issuing bootstrap certificates.
	// For safety, we do not allow client certificates (issued by the system) to request bootstrap certificates at this time.
	// This restriction will stay in place until we analyze and design proper support for allowing other client certificates
	// to issue bootstrap certificates safely.
	if _, err := PeerCertificateFromCtx(ctx); err == nil {
		return fmt.Errorf("bootstrap certificates cannot be requested using client certificates issued by the system")
	}
	return nil
}

func (s *SignerClientBootstrap) Sign(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
	cfg := s.ca.Config()

	if request.ResourceName() == nil {
		return nil, fmt.Errorf("request is missing metadata.name")
	}

	// the CN will need the enrollment prefix applied;
	// if the certificate is being renewed, the name will have an existing prefix.
	// we do not touch in this case.

	x509CSR := request.X509()
	u := x509CSR.Subject.CommonName

	// Once we move all prefixes/name formation to the client this can become a simple
	if BootstrapCNFromName(cfg, u) != BootstrapCNFromName(cfg, *request.ResourceName()) {
		return nil, fmt.Errorf("%w - CN %s Metadata %s mismatch", flterrors.ErrSignCert, u, *request.ResourceName())
	}

	// Create a copy to modify the CN
	x509CSR.Subject.CommonName = BootstrapCNFromName(cfg, u)

	expiry := DefaultEnrollmentCertExpirySeconds
	if request.ExpirationSeconds() != nil {
		expiry = *request.ExpirationSeconds()
	}

	return s.ca.IssueRequestedClientCertificate(ctx, &x509CSR, int(expiry))
}
