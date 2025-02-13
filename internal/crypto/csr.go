package crypto

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	oscrypto "github.com/openshift/library-go/pkg/crypto"
)

func MakeCSR(privateKey crypto.Signer, subjectName string) ([]byte, error) {
	template := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: subjectName},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, privateKey)
	if err != nil {
		return nil, err
	}

	csrPemBlock := &pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	}

	return pem.EncodeToMemory(csrPemBlock), nil
}

func ParseCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, rest := pem.Decode(csrPEM)
	if block == nil || len(bytes.TrimSpace(rest)) > 0 {
		return nil, flterrors.ErrInvalidPEMBlock
	}

	var csr *x509.CertificateRequest
	var err error
	switch block.Type {
	case "CERTIFICATE REQUEST":
		csr, err = x509.ParseCertificateRequest(block.Bytes)
	default:
		return nil, fmt.Errorf("%w: %s", flterrors.ErrUnknownPEMType, block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrCSRParse, err.Error())
	}
	return csr, nil
}

// IssueRequestedClientCertificate issues a client certificate based on the provided
// Certificate Signing Request (CSR) and the desired expiration time in seconds.
// This currently processes both enrollment cert and management cert signing requests, which both are signed
// by the FC service's internal CA instance named 'ca'.


func (ca *CABackend) IssueRequestedCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int, usage []x509.ExtKeyUsage) (*x509.Certificate, error) {
	now := time.Now()
	template := &x509.Certificate{
		Subject: csr.Subject,

		Signature:          csr.Signature,
		SignatureAlgorithm: csr.SignatureAlgorithm,

		PublicKey:          csr.PublicKey,
		PublicKeyAlgorithm: csr.PublicKeyAlgorithm,

		Issuer: ca.Config.Certs[0].Subject,

		NotBefore:    now.Add(-1 * time.Second),
		NotAfter:     now.Add(time.Duration(expirySeconds) * time.Second),
		SerialNumber: big.NewInt(1),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           usage,
		BasicConstraintsValid: true,

		AuthorityKeyId: ca.Config.Certs[0].SubjectKeyId,
	}
	if len(csr.IPAddresses) > 0 {
		template.IPAddresses = csr.IPAddresses
	}
	if len(csr.DNSNames) > 0 {
		template.DNSNames = csr.DNSNames
	}

	cert, err := ca.signCertificate(template, csr.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrSignCert, err.Error())
	}
	return cert, nil
}

func (ca *CABackend) IssueRequestedServerCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int) (*x509.Certificate, error) {
	return ca.IssueRequestedCertificateAsX509(csr, expirySeconds, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth})
}

func (ca *CABackend) IssueRequestedClientCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int) (*x509.Certificate, error) {
	return ca.IssueRequestedCertificateAsX509(csr, expirySeconds, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
}

func (ca *CABackend) IssueRequestedClientCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error) {

	x509, err := ca.IssueRequestedClientCertificateAsX509(csr, expirySeconds)

	if err != nil {
		return nil, err
	}
	certData, err := oscrypto.EncodeCertificates(x509)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrEncodeCert, err.Error())
	}
	return certData, nil
}
func (ca *CABackend) IssueRequestedServerCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error) {

	x509, err := ca.IssueRequestedServerCertificateAsX509(csr, expirySeconds)

	if err != nil {
		return nil, err
	}
	certData, err := oscrypto.EncodeCertificates(x509)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrEncodeCert, err.Error())
	}
	return certData, nil
}


func (ca *CABackend) GetCABundleX509() ([]*x509.Certificate) {
	return ca.Config.Certs
}
