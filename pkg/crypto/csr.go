package crypto

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"

	"github.com/flightctl/flightctl/internal/flterrors"
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
		return nil, fmt.Errorf("%w: %w", flterrors.ErrCSRParse, err)
	}
	return csr, nil
}

// GetExtensionValueFromCSR returns the string value stored in the given extension
// (identified by its ASN.1 OID) inside the provided x509.CertificateRequest. The
// function searches both Extensions and ExtraExtensions slices. If the extension
// is not found, or if unmarshalling fails, an error is returned.
func GetExtensionValueFromCSR(csr *x509.CertificateRequest, oid asn1.ObjectIdentifier) ([]byte, error) {
	for _, ext := range append(csr.Extensions, csr.ExtraExtensions...) {
		if ext.Id.Equal(oid) {
			return ext.Value, nil
		}
	}
	return nil, flterrors.ErrExtensionNotFound
}
