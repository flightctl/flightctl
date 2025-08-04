package signer

import (
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/flterrors"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	oscrypto "github.com/openshift/library-go/pkg/crypto"
)

// EncodeCertificatePEM encodes a single x509.Certificate into PEM bytes.
func EncodeCertificatePEM(cert *x509.Certificate) ([]byte, error) {
	b, err := oscrypto.EncodeCertificates(cert)
	if err != nil {
		return nil, errors.Join(flterrors.ErrEncodeCert, err)
	}
	return b, nil
}

// ParseCertificatePEM parses a single x509.Certificate from PEM bytes.
func ParseCertificatePEM(certPEM []byte) (*x509.Certificate, error) {
	block, err := fccrypto.GetPEMBlock(certPEM)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}
	return cert, nil
}
