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
func (ca *CA) IssueRequestedClientCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error) {
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
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,

		AuthorityKeyId: ca.Config.Certs[0].SubjectKeyId,
	}
	cert, err := ca.signCertificate(template, csr.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrSignCert, err.Error())
	}
	certData, err := oscrypto.EncodeCertificates(cert)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrEncodeCert, err.Error())
	}

	return certData, nil
}
