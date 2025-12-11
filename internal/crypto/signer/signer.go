package signer

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/crypto"
)

type Signer interface {
	Name() string
	Verify(ctx context.Context, request SignRequest) error
	Sign(ctx context.Context, request SignRequest) (*x509.Certificate, error)
}

type RestrictedSigner interface {
	RestrictedPrefix() string
}

type CA interface {
	Config() *ca.Config
	GetSigner(name string) Signer
	PeerCertificateSignerFromCtx(ctx context.Context) Signer
	IssueRequestedClientCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...certOption) (*x509.Certificate, error)
	IssueRequestedServerCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...certOption) (*x509.Certificate, error)
}

type CASigners struct {
	ca                 CA
	signers            map[string]Signer
	restrictedPrefixes map[string]Signer
}

func NewCASigners(ca CA) *CASigners {
	cfg := ca.Config()

	ret := &CASigners{
		ca: ca,
		signers: map[string]Signer{
			cfg.DeviceEnrollmentSignerName: WithSignerNameValidation(
				WithCertificateReuse(
					WithCSRValidation(
						WithSignerNameExtension(
							WithOrgIDExtension(NewDeviceEnrollment))(ca),
					),
				),
			),
			cfg.DeviceManagementSignerName: WithSignerNameValidation(
				WithCertificateReuse(
					WithCSRValidation(
						WithSignerNameExtension(WithOrgIDExtension(NewSignerDeviceManagement))(ca),
					),
				),
			),
			cfg.DeviceSvcClientSignerName: WithSignerNameValidation(
				WithCertificateReuse(
					WithCSRValidation(
						WithSignerNameExtension(NewSignerDeviceSvcClient)(ca),
					),
				),
			),
			cfg.ServerSvcSignerName: WithSignerNameValidation(
				WithCertificateReuse(
					WithCSRValidation(
						WithSignerNameExtension(NewSignerServerSvc)(ca),
					),
				),
			),
		},
	}

	ret.restrictedPrefixes = make(map[string]Signer)
	for _, s := range ret.signers {
		if rs, ok := findRestrictedSigner(s); ok {
			if p := rs.RestrictedPrefix(); p != "" {
				if _, ok := ret.restrictedPrefixes[p]; ok {
					panic(fmt.Sprintf("duplicate restricted prefix %q found in signer %q", p, s.Name()))
				}
				ret.restrictedPrefixes[p] = s
			}
		}
	}

	if len(ret.restrictedPrefixes) > 0 {
		for name, signer := range ret.signers {
			ret.signers[name] = WithSignerRestrictedPrefixes(ret.restrictedPrefixes, signer)
		}
	}
	return ret
}

func (s *CASigners) GetSigner(name string) Signer {
	return s.signers[name]
}

type chainSignerCA struct {
	next                            CA
	issueRequestedClientCertificate func(context.Context, *x509.CertificateRequest, int, ...certOption) (*x509.Certificate, error)
	issueRequestedServerCertificate func(context.Context, *x509.CertificateRequest, int, ...certOption) (*x509.Certificate, error)
}

type chainSigner struct {
	next   Signer
	name   func() string
	verify func(context.Context, SignRequest) error
	sign   func(context.Context, SignRequest) (*x509.Certificate, error)
}

func (s *chainSignerCA) Config() *ca.Config {
	return s.next.Config()
}

func (s *chainSignerCA) GetSigner(name string) Signer {
	return s.next.GetSigner(name)
}

func (s *chainSignerCA) PeerCertificateSignerFromCtx(ctx context.Context) Signer {
	return s.next.PeerCertificateSignerFromCtx(ctx)
}

func (s *chainSignerCA) IssueRequestedClientCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...certOption) (*x509.Certificate, error) {
	if s.issueRequestedClientCertificate != nil {
		return s.issueRequestedClientCertificate(ctx, csr, expirySeconds, opts...)
	}
	return s.next.IssueRequestedClientCertificate(ctx, csr, expirySeconds, opts...)
}

func (s *chainSignerCA) IssueRequestedServerCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...certOption) (*x509.Certificate, error) {
	if s.issueRequestedServerCertificate != nil {
		return s.issueRequestedServerCertificate(ctx, csr, expirySeconds, opts...)
	}
	return s.next.IssueRequestedServerCertificate(ctx, csr, expirySeconds, opts...)
}

func (s *chainSigner) Name() string {
	if s.name != nil {
		return s.name()
	}
	return s.next.Name()
}

func (s *chainSigner) Verify(ctx context.Context, request SignRequest) error {
	if s.verify != nil {
		return s.verify(ctx, request)
	}
	return s.next.Verify(ctx, request)
}

func (s *chainSigner) Sign(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
	if s.sign != nil {
		return s.sign(ctx, request)
	}
	return s.next.Sign(ctx, request)
}

func WithSignerNameValidation(s Signer) Signer {
	return &chainSigner{
		next: s,
		verify: func(ctx context.Context, request SignRequest) error {
			if request.SignerName() != s.Name() {
				return fmt.Errorf("signer name mismatch: got %q, expected %q", request.SignerName(), s.Name())
			}
			return s.Verify(ctx, request)
		},
		sign: func(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
			if request.SignerName() != s.Name() {
				return nil, fmt.Errorf("signer name mismatch: got %q, expected %q", request.SignerName(), s.Name())
			}
			return s.Sign(ctx, request)
		},
	}
}

func WithCertificateReuse(s Signer) Signer {
	return &chainSigner{
		next: s,
		sign: func(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
			if cert, ok := request.IssuedCertificate(); ok {
				return cert, nil
			}
			return s.Sign(ctx, request)
		},
	}
}

func WithCSRValidation(s Signer) Signer {
	return &chainSigner{
		next: s,
		verify: func(ctx context.Context, request SignRequest) error {
			x509CSR := request.X509()
			if err := crypto.ValidateX509CSR(&x509CSR); err != nil {
				return err
			}
			return s.Verify(ctx, request)
		},
		sign: func(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
			x509CSR := request.X509()
			if err := crypto.ValidateX509CSR(&x509CSR); err != nil {
				return nil, err
			}
			return s.Sign(ctx, request)
		},
	}
}

type ctxKey string

const CertificateSignerNameCtxKey ctxKey = "certificate_signer"

func WithSignerNameExtension(s func(CA) Signer) func(CA) Signer {
	return func(ca CA) Signer {
		inst := s(&chainSignerCA{
			next: ca,
			issueRequestedClientCertificate: func(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...certOption) (*x509.Certificate, error) {
				signerName, ok := ctx.Value(CertificateSignerNameCtxKey).(string)
				if !ok || signerName == "" {
					return nil, fmt.Errorf("certificate signer name not found in context")
				}
				return ca.IssueRequestedClientCertificate(ctx, csr, expirySeconds, append(opts, WithExtension(OIDSignerName, signerName))...)
			},
		})
		return &chainSigner{
			next: inst,
			sign: func(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
				ctx = context.WithValue(ctx, CertificateSignerNameCtxKey, inst.Name())
				return inst.Sign(ctx, request)
			},
		}
	}
}

// WithOrgIDExtension Injects OrgID extension (from CSR or context) when issuing client certificates via CA.
// Rules:
// - If both CSR and context contain OrgID and they differ: fail verification/issuance.
// - If CSR is missing OrgID and context has one: use context OrgID.
// - If CSR has OrgID: use it.
// - If neither has OrgID: do not include OrgID in the certificate.
func WithOrgIDExtension(s func(CA) Signer) func(CA) Signer {
	return func(ca CA) Signer {
		inst := s(&chainSignerCA{
			next: ca,
			issueRequestedClientCertificate: func(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...certOption) (*x509.Certificate, error) {
				csrOrg, csrHas, err := GetOrgIDExtensionFromCSR(csr)
				if err != nil {
					return nil, errors.Join(flterrors.ErrCSRInvalid, fmt.Errorf("get OrgID from CSR: %w", err))
				}

				ctxOrg, ctxHas := util.GetOrgIdFromContext(ctx)

				if csrHas && ctxHas && csrOrg != ctxOrg {
					return nil, errors.Join(flterrors.ErrCSRInvalid,
						fmt.Errorf("organization ID mismatch: %s != %s", csrOrg, ctxOrg))
				}

				if csrHas {
					opts = append(opts, WithExtension(OIDOrgID, csrOrg.String()))
				} else if ctxHas {
					opts = append(opts, WithExtension(OIDOrgID, ctxOrg.String()))
				}

				return ca.IssueRequestedClientCertificate(ctx, csr, expirySeconds, opts...)
			},
		})

		return &chainSigner{
			next: inst,
			verify: func(ctx context.Context, req SignRequest) error {
				csr := req.X509()

				csrOrg, csrHas, err := GetOrgIDExtensionFromCSR(&csr)
				if err != nil {
					return errors.Join(flterrors.ErrCSRInvalid, fmt.Errorf("get OrgID from CSR: %w", err))
				}
				ctxOrg, ctxHas := util.GetOrgIdFromContext(ctx)

				if csrHas && ctxHas && csrOrg != ctxOrg {
					return errors.Join(flterrors.ErrCSRInvalid,
						fmt.Errorf("organization ID mismatch: %s != %s", csrOrg, ctxOrg))
				}

				return inst.Verify(ctx, req)
			},
			sign: func(ctx context.Context, req SignRequest) (*x509.Certificate, error) {
				return inst.Sign(ctx, req)
			},
		}
	}
}

func WithSignerRestrictedPrefixes(restrictedPrefixes map[string]Signer, s Signer) Signer {
	checkPrefixes := func(cert *x509.CertificateRequest) error {
		for p, restrictedSigner := range restrictedPrefixes {
			if strings.HasPrefix(cert.Subject.CommonName, p) && restrictedSigner != s {
				return fmt.Errorf("common name prefix %q is restricted to signer %q, but requested by signer %q",
					p, restrictedSigner.Name(), s.Name())
			}
		}
		return nil
	}

	return &chainSigner{
		next: s,
		verify: func(ctx context.Context, request SignRequest) error {
			x509CSR := request.X509()
			if err := checkPrefixes(&x509CSR); err != nil {
				return err
			}
			return s.Verify(ctx, request)
		},
		sign: func(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
			x509CSR := request.X509()
			if err := checkPrefixes(&x509CSR); err != nil {
				return nil, err
			}
			return s.Sign(ctx, request)
		},
	}
}

func findRestrictedSigner(s Signer) (RestrictedSigner, bool) {
	for {
		if rs, ok := s.(RestrictedSigner); ok {
			return rs, true
		}

		chain, ok := s.(*chainSigner)
		if !ok || chain.next == nil {
			break
		}
		s = chain.next
	}

	return nil, false
}
