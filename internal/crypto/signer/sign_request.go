package signer

import (
	"crypto/x509"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

// SignRequest represents the minimal interface needed for certificate signing operations.
type SignRequest interface {
	SignerName() string
	ResourceName() *string
	X509() x509.CertificateRequest
	ExpirationSeconds() *int32
	IssuedCertificate() (*x509.Certificate, bool)
}

type basicSignRequest struct {
	signerName   *string
	x509csr      *x509.CertificateRequest
	expiry       *int32
	resourceName *string
	issuedCert   *x509.Certificate
}

var _ SignRequest = (*basicSignRequest)(nil)

func (r *basicSignRequest) SignerName() string            { return *r.signerName }
func (r *basicSignRequest) ResourceName() *string         { return r.resourceName }
func (r *basicSignRequest) X509() x509.CertificateRequest { return *r.x509csr }
func (r *basicSignRequest) ExpirationSeconds() *int32     { return r.expiry }
func (r *basicSignRequest) IssuedCertificate() (*x509.Certificate, bool) {
	return r.issuedCert, r.issuedCert != nil
}

type SignRequestOption func(*basicSignRequest) error

// NewSignRequest constructs a new SignRequest using the provided signer name and CSR.
// Additional attributes can be supplied via functional options.
func NewSignRequest(signerName string, csr x509.CertificateRequest, opts ...SignRequestOption) (SignRequest, error) {
	req := &basicSignRequest{
		signerName: &signerName,
		x509csr:    &csr,
	}

	for _, opt := range opts {
		if err := opt(req); err != nil {
			return nil, err
		}
	}

	return req, nil
}

func NewSignRequestFromBytes(signerName string, csrBytes []byte, opts ...SignRequestOption) (SignRequest, error) {
	csr, err := fccrypto.ParseCSR(csrBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid CSR: %w", err)
	}

	req, err := NewSignRequest(signerName, *csr, opts...)
	if err != nil {
		return nil, err
	}

	return req, nil
}

func NewSignRequestFromEnrollment(er *api.EnrollmentRequest, signerName string, opts ...SignRequestOption) (SignRequest, error) {
	var defaultOpts []SignRequestOption
	if er.Status != nil && er.Status.Certificate != nil {
		certBytes := []byte(*er.Status.Certificate)
		defaultOpts = append(defaultOpts, WithIssuedCertificateBytes(certBytes))
	}

	if er.Metadata.Name != nil {
		defaultOpts = append(defaultOpts, WithResourceName(*er.Metadata.Name))
	}

	opts = append(defaultOpts, opts...)

	return NewSignRequestFromBytes(signerName, []byte(er.Spec.Csr), opts...)
}

func NewSignRequestFromCertificateSigningRequest(csr *api.CertificateSigningRequest, opts ...SignRequestOption) (SignRequest, error) {
	var defaultOpts []SignRequestOption

	if csr.Status != nil && csr.Status.Certificate != nil {
		defaultOpts = append(defaultOpts, WithIssuedCertificateBytes(*csr.Status.Certificate))
	}

	if csr.Spec.ExpirationSeconds != nil {
		defaultOpts = append(defaultOpts, WithExpirationSeconds(*csr.Spec.ExpirationSeconds))
	}

	if csr.Metadata.Name != nil {
		defaultOpts = append(defaultOpts, WithResourceName(*csr.Metadata.Name))
	}

	opts = append(defaultOpts, opts...)

	return NewSignRequestFromBytes(csr.Spec.SignerName, csr.Spec.Request, opts...)
}

// WithExpirationSeconds sets the certificate expiry (in seconds) for the sign request.
func WithExpirationSeconds(expiry int32) SignRequestOption {
	return func(r *basicSignRequest) error {
		r.expiry = &expiry
		return nil
	}
}

// WithResourceName sets the original resource name for the sign request.
func WithResourceName(name string) SignRequestOption {
	return func(r *basicSignRequest) error {
		r.resourceName = &name
		return nil
	}
}

// WithIssuedCertificate attaches an already-issued certificate to the request
func WithIssuedCertificate(cert *x509.Certificate) SignRequestOption {
	return func(r *basicSignRequest) error {
		r.issuedCert = cert
		return nil
	}
}

func WithIssuedCertificateBytes(certBytes []byte) SignRequestOption {
	return func(r *basicSignRequest) error {
		if len(certBytes) == 0 {
			return nil // No certificate bytes provided, nothing to do
		}
		cert, err := ParseCertificatePEM(certBytes)
		if err != nil {
			return fmt.Errorf("invalid certificate: %w", err)
		}

		return WithIssuedCertificate(cert)(r)
	}
}
