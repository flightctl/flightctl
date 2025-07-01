package signer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
)

type SignerClientBootstrap struct {
	name string
	ca   CA
}

func NewClientBootstrap(CAClient CA) Signer {
	cfg := CAClient.Config()
	return &SignerClientBootstrap{name: cfg.ClientBootstrapSignerName, ca: CAClient}
}

func (s *SignerClientBootstrap) RestrictedPrefix() string {
	return s.ca.Config().ClientBootstrapCommonNamePrefix
}

func (s *SignerClientBootstrap) Name() string {
	return s.name
}

func (s *SignerClientBootstrap) Verify(ctx context.Context, request api.CertificateSigningRequest) error {
	// Crypto validation
	cn, ok := ctx.Value(consts.TLSCommonNameCtxKey).(string)

	// Note - if auth is disabled and there is no mTLS handshake we get ok == False.
	// We cannot check anything in that case.

	if ok {
		cfg := s.ca.Config()
		if request.Spec.SignerName != cfg.ClientBootstrapSignerName {
			if request.Metadata.Name == nil {
				return errors.New("invalid csr record - no name in metadata")
			}
			if cn != bootstrapCNFromName(cfg, *request.Metadata.Name) {
				return errors.New("denied attempt to renew other entity certificate")
			}
		}
	}
	return nil
}

func (s *SignerClientBootstrap) Sign(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
	cfg := s.ca.Config()

	if request.Status.Certificate != nil && len(*request.Status.Certificate) > 0 {
		return *request.Status.Certificate, nil
	}

	csr, err := fcrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, err
	}

	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrSignature, err)
	}

	// the CN will need the enrollment prefix applied;
	// if the certificate is being renewed, the name will have an existing prefix.
	// we do not touch in this case.

	u := csr.Subject.CommonName

	// Once we move all prefixes/name formation to the client this can become a simple
	// comparison of u and *request.Metadata.Name

	if bootstrapCNFromName(cfg, u) != bootstrapCNFromName(cfg, *request.Metadata.Name) {
		return nil, fmt.Errorf("%w - CN %s Metadata %s mismatch", flterrors.ErrSignCert, u, *request.Metadata.Name)
	}

	csr.Subject.CommonName = bootstrapCNFromName(cfg, u)

	var expiry int32 = 60 * 60 * 24 * 7 // 7 days
	if request.Spec.ExpirationSeconds != nil {
		expiry = *request.Spec.ExpirationSeconds
	}

	certData, err := s.ca.IssueRequestedClientCertificate(ctx, csr, int(expiry))
	if err != nil {
		return nil, err
	}

	return certData, nil
}

func bootstrapCNFromName(cfg *ca.Config, name string) string {
	if cfg == nil {
		return ""
	}

	base := []string{cfg.ClientBootstrapCommonNamePrefix, cfg.DeviceCommonNamePrefix}
	for _, prefix := range append(base, cfg.ExtraAllowedPrefixes...) {
		if strings.HasPrefix(name, prefix) {
			return name
		}
	}
	return cfg.ClientBootstrapCommonNamePrefix + name
}
