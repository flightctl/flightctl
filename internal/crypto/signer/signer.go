package signer

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
)

type Signer interface {
	Name() string
	Verify(ctx context.Context, csr api.CertificateSigningRequest) error
	Sign(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error)
}

type RestrictedSigner interface {
	RestrictedPrefix() string
}

type CA interface {
	Config() *ca.Config
	GetSigner(name string) Signer
	GetSignerFromCtx(ctx context.Context) Signer
	IssueRequestedClientCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...CertOption) ([]byte, error)
	IssueRequestedServerCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...CertOption) ([]byte, error)
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
			cfg.ClientBootstrapSignerName: WithSignerNameValidation(
				WithCSRValidation(
					WithSignerNameExtension(NewClientBootstrap, ca),
				),
			),
			cfg.DeviceEnrollmentSignerName: WithSignerNameValidation(
				WithCSRValidation(
					WithSignerNameExtension(NewSignerDeviceEnrollment, ca),
				),
			),
			cfg.DeviceSvcClientSignerName: WithSignerNameValidation(
				WithCSRValidation(
					WithSignerNameExtension(NewSignerDeviceSvcClient, ca),
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
	issueRequestedClientCertificate func(context.Context, *x509.CertificateRequest, int, ...CertOption) ([]byte, error)
	issueRequestedServerCertificate func(context.Context, *x509.CertificateRequest, int, ...CertOption) ([]byte, error)
}

type chainSigner struct {
	next   Signer
	name   func() string
	verify func(context.Context, api.CertificateSigningRequest) error
	sign   func(context.Context, api.CertificateSigningRequest) ([]byte, error)
}

func (s *chainSignerCA) Config() *ca.Config {
	return s.next.Config()
}

func (s *chainSignerCA) GetSigner(name string) Signer {
	return s.next.GetSigner(name)
}

func (s *chainSignerCA) GetSignerFromCtx(ctx context.Context) Signer {
	return s.next.GetSignerFromCtx(ctx)
}

func (s *chainSignerCA) IssueRequestedClientCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...CertOption) ([]byte, error) {
	if s.issueRequestedClientCertificate != nil {
		return s.issueRequestedClientCertificate(ctx, csr, expirySeconds, opts...)
	}
	return s.next.IssueRequestedClientCertificate(ctx, csr, expirySeconds, opts...)
}

func (s *chainSignerCA) IssueRequestedServerCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...CertOption) ([]byte, error) {
	if s.issueRequestedClientCertificate != nil {
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

func (s *chainSigner) Verify(ctx context.Context, request api.CertificateSigningRequest) error {
	if s.verify != nil {
		return s.verify(ctx, request)
	}
	return s.next.Verify(ctx, request)

}

func (s *chainSigner) Sign(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
	if s.sign != nil {
		return s.sign(ctx, request)
	}
	return s.next.Sign(ctx, request)
}

func WithSignerNameExtension(s func(CA) Signer, ca CA) Signer {
	inst := s(&chainSignerCA{
		next: ca,
		issueRequestedClientCertificate: func(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...CertOption) ([]byte, error) {
			signerName, ok := ctx.Value(consts.CertificateSignerNameCtxKey).(string)
			if !ok || signerName == "" {
				return nil, fmt.Errorf("certificate signer name not found in context")
			}
			return ca.IssueRequestedClientCertificate(ctx, csr, expirySeconds, append(opts, WithExtension(OIDSignerName, signerName))...)
		},
	})
	return &chainSigner{
		next: inst,
		sign: func(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
			// Inject signer name into context before Sign calls IssueRequestedClientCertificate
			ctx = context.WithValue(ctx, consts.CertificateSignerNameCtxKey, inst.Name())
			return inst.Sign(ctx, request)
		},
	}
}

func WithCSRValidation(s Signer) Signer {
	return &chainSigner{
		next: s,
		verify: func(ctx context.Context, request api.CertificateSigningRequest) error {
			if errs := request.Validate(); len(errs) > 0 {
				return errors.Join(errs...)
			}
			return s.Verify(ctx, request)
		},
		sign: func(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
			if errs := request.Validate(); len(errs) > 0 {
				return nil, errors.Join(errs...)
			}
			return s.Sign(ctx, request)
		},
	}
}

func WithSignerNameValidation(s Signer) Signer {
	return &chainSigner{
		next: s,
		verify: func(ctx context.Context, request api.CertificateSigningRequest) error {
			if request.Spec.SignerName != s.Name() {
				return fmt.Errorf("signer name mismatch: got %q, expected %q", request.Spec.SignerName, s.Name())
			}
			return s.Verify(ctx, request)
		},
		sign: func(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
			if request.Spec.SignerName != s.Name() {
				return nil, fmt.Errorf("signer name mismatch: got %q, expected %q", request.Spec.SignerName, s.Name())
			}
			return s.Sign(ctx, request)
		},
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
		verify: func(ctx context.Context, request api.CertificateSigningRequest) error {
			cert, err := fcrypto.ParseCSR(request.Spec.Request)
			if err != nil {
				return fmt.Errorf("invalid CSR data: %w", err)
			}

			if err := checkPrefixes(cert); err != nil {
				return err
			}
			return s.Verify(ctx, request)
		},
		sign: func(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
			cert, err := fcrypto.ParseCSR(request.Spec.Request)
			if err != nil {
				return nil, fmt.Errorf("invalid CSR data: %w", err)
			}

			if err := checkPrefixes(cert); err != nil {
				return nil, err
			}
			return s.Sign(ctx, request)
		},
	}
}

func findRestrictedSigner(s Signer) (RestrictedSigner, bool) {
	for {
		// Check current signer
		if rs, ok := s.(RestrictedSigner); ok {
			return rs, true
		}

		// Check if we can go deeper (i.e., s is a chainSigner)
		chain, ok := s.(*chainSigner)
		if !ok || chain.next == nil {
			break // Can't go deeper
		}

		// Move to next
		s = chain.next
	}

	return nil, false
}
