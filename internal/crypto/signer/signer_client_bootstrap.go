package signer

import (
	"context"
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

func (s *SignerClientBootstrap) Verify(ctx context.Context, request *Request) error {
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

func (s *SignerClientBootstrap) Sign(ctx context.Context, request *Request) ([]byte, error) {
	cfg := s.ca.Config()

	if request.API.Metadata.Name == nil {
		return nil, fmt.Errorf("request is missing metadata.name")
	}

	// the CN will need the enrollment prefix applied;
	// if the certificate is being renewed, the name will have an existing prefix.
	// we do not touch in this case.

	x509CSR := request.X509()
	u := x509CSR.Subject.CommonName

	// Once we move all prefixes/name formation to the client this can become a simple
	// comparison of u and *request.Api.Metadata.Name

	if BootstrapCNFromName(cfg, u) != BootstrapCNFromName(cfg, *request.API.Metadata.Name) {
		return nil, fmt.Errorf("%w - CN %s Metadata %s mismatch", flterrors.ErrSignCert, u, *request.API.Metadata.Name)
	}

	// Create a copy to modify the CN
	x509CSR.Subject.CommonName = BootstrapCNFromName(cfg, u)

	expiry := DefaultEnrollmentCertExpirySeconds
	if request.API.Spec.ExpirationSeconds != nil {
		expiry = *request.API.Spec.ExpirationSeconds
	}

	certData, err := s.ca.IssueRequestedClientCertificate(ctx, &x509CSR, int(expiry))
	if err != nil {
		return nil, err
	}

	return certData, nil
}
