package signer

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/pkg/crypto"
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

func (s *SignerClientBootstrap) Verify(ctx context.Context, request api.CertificateSigningRequest) error {
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

func (s *SignerClientBootstrap) Sign(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
	cfg := s.ca.Config()

	if request.Metadata.Name == nil {
		return nil, fmt.Errorf("request is missing metadata.name")
	}

	csr, err := crypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, err
	}

	// the CN will need the enrollment prefix applied;
	// if the certificate is being renewed, the name will have an existing prefix.
	// we do not touch in this case.

	u := csr.Subject.CommonName

	// Once we move all prefixes/name formation to the client this can become a simple
	// comparison of u and *request.Metadata.Name

	if BootstrapCNFromName(cfg, u) != BootstrapCNFromName(cfg, *request.Metadata.Name) {
		return nil, fmt.Errorf("%w - CN %s Metadata %s mismatch", flterrors.ErrSignCert, u, *request.Metadata.Name)
	}

	csr.Subject.CommonName = BootstrapCNFromName(cfg, u)

	expiry := DefaultEnrollmentCertExpirySeconds
	if request.Spec.ExpirationSeconds != nil {
		expiry = *request.Spec.ExpirationSeconds
	}

	certData, err := s.ca.IssueRequestedClientCertificate(ctx, csr, int(expiry))
	if err != nil {
		return nil, err
	}

	return certData, nil
}
