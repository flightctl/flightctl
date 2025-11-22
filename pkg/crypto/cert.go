package crypto

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/flterrors"
)

// EncodeCertificatePEM encodes a single x509.Certificate into PEM bytes.
func EncodeCertificatePEM(cert *x509.Certificate) ([]byte, error) {
	if cert == nil {
		return nil, flterrors.ErrResourceIsNil
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})
	if pemBytes == nil {
		return nil, flterrors.ErrEncodeCert
	}
	return pemBytes, nil
}

// parseCertificateFromBlock parses a single x509.Certificate from a PEM block.
func parseCertificateFromBlock(block *pem.Block) (*x509.Certificate, error) {
	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrUnknownPEMType, block.Type)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.Join(flterrors.ErrParseCert, err)
	}

	return cert, nil
}

// ParseCertificatePEM parses a single x509.Certificate from PEM bytes.
func ParseCertificatePEM(certPEM []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(certPEM)
	if block == nil || len(bytes.TrimSpace(rest)) > 0 {
		return nil, flterrors.ErrInvalidPEMBlock
	}

	return parseCertificateFromBlock(block)
}

// ParseCertsPEM extracts multiple x509.Certificates from PEM-encoded byte arrays.
// Returns an error if parsing fails or if no valid certificates are found.
func ParseCertsPEM(pemCerts []byte) ([]*x509.Certificate, error) {
	ok := false
	certs := []*x509.Certificate{}
	for len(pemCerts) > 0 {
		var block *pem.Block
		block, pemCerts = pem.Decode(pemCerts)
		if block == nil {
			break
		}
		// Only use PEM "CERTIFICATE" blocks without extra headers
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			continue
		}

		cert, err := parseCertificateFromBlock(block)
		if err != nil {
			return certs, err
		}

		certs = append(certs, cert)
		ok = true
	}

	if !ok {
		return certs, errors.New("data does not contain any valid RSA or ECDSA certificates")
	}
	return certs, nil
}

// NewPoolFromBytes creates an x509.CertPool from PEM-encoded certificate bytes.
// Returns an error if the certificates could not be parsed or if no valid certificates are found.
func NewPoolFromBytes(pemBlock []byte) (*x509.CertPool, error) {
	certs, err := ParseCertsPEM(pemBlock)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	for _, cert := range certs {
		pool.AddCert(cert)
	}
	return pool, nil
}
