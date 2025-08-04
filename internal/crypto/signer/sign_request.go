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
	X509() x509.CertificateRequest
	ExpirationSeconds() *int32
	ResourceName() *string
	IssuedCertificate() (*x509.Certificate, bool)
}

type basicSignRequest struct {
	signerName   string
	x509csr      x509.CertificateRequest
	expiry       *int32
	resourceName *string
	issuedCert   *x509.Certificate
}

// Compile-time check that basicSignRequest implements SignRequest.
var _ SignRequest = (*basicSignRequest)(nil)

func NewSignRequest(signerName string, csr x509.CertificateRequest, expiry *int32, resourceName *string, issuedCert *x509.Certificate) SignRequest {
	return &basicSignRequest{
		signerName:   signerName,
		x509csr:      csr,
		expiry:       expiry,
		resourceName: resourceName,
		issuedCert:   issuedCert,
	}
}

func NewSignRequestFromBytes(signerName string, csrBytes []byte, expiry *int32, resourceName *string, certBytes []byte) (SignRequest, error) {
	x509CSR, err := fccrypto.ParseCSR(csrBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid CSR: %w", err)
	}

	var issuedCert *x509.Certificate
	if len(certBytes) > 0 {
		cert, err := ParseCertificatePEM(certBytes)
		if err != nil {
			return nil, fmt.Errorf("invalid certificate: %w", err)
		}
		issuedCert = cert
	}

	return NewSignRequest(signerName, *x509CSR, expiry, resourceName, issuedCert), nil
}

// basicSignRequest interface implementations
func (r *basicSignRequest) SignerName() string            { return r.signerName }
func (r *basicSignRequest) X509() x509.CertificateRequest { return r.x509csr }
func (r *basicSignRequest) ExpirationSeconds() *int32     { return r.expiry }
func (r *basicSignRequest) ResourceName() *string         { return r.resourceName }
func (r *basicSignRequest) IssuedCertificate() (*x509.Certificate, bool) {
	return r.issuedCert, r.issuedCert != nil
}

func NewSignRequestFromEnrollment(er *api.EnrollmentRequest, signerName string) (SignRequest, error) {
	var certBytes []byte
	if er.Status != nil && er.Status.Certificate != nil {
		certBytes = []byte(*er.Status.Certificate)
	}
	return NewSignRequestFromBytes(signerName, []byte(er.Spec.Csr), nil, er.Metadata.Name, certBytes)
}

func NewSignRequestFromCertificateSigningRequest(csr *api.CertificateSigningRequest) (SignRequest, error) {
	var certBytes []byte
	if csr.Status != nil && csr.Status.Certificate != nil {
		certBytes = *csr.Status.Certificate
	}
	return NewSignRequestFromBytes(csr.Spec.SignerName, csr.Spec.Request, csr.Spec.ExpirationSeconds, csr.Metadata.Name, certBytes)
}
