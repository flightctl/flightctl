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

// ParseCertificatePEM parses a single x509.Certificate from PEM bytes.
func ParseCertificatePEM(certPEM []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(certPEM)
	if block == nil || len(bytes.TrimSpace(rest)) > 0 {
		return nil, flterrors.ErrInvalidPEMBlock
	}

	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrUnknownPEMType, block.Type)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.Join(flterrors.ErrParseCert, err)
	}

	return cert, nil
}
