package crypto

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"

	"github.com/flightctl/flightctl/internal/flterrors"
)

func MakeCSR(privateKey crypto.Signer, subjectName string) ([]byte, error) {
	algo, err := selectSignatureAlgorithm(privateKey)
	if err != nil {
		return nil, fmt.Errorf("selecting signature algorithm: %w", err)
	}

	template := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: subjectName},
		SignatureAlgorithm: algo,
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, privateKey)
	if err != nil {
		return nil, fmt.Errorf("generating standard CSR: %w", err)
	}

	csrPemBlock := &pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	}

	return pem.EncodeToMemory(csrPemBlock), nil
}

func selectSignatureAlgorithm(signer crypto.Signer) (x509.SignatureAlgorithm, error) {
	switch pub := signer.Public().(type) {
	case *ecdsa.PublicKey:
		switch pub.Curve {
		case elliptic.P256():
			return x509.ECDSAWithSHA256, nil
		case elliptic.P384():
			return x509.ECDSAWithSHA384, nil
		case elliptic.P521():
			return x509.ECDSAWithSHA512, nil
		default:
			return x509.UnknownSignatureAlgorithm, fmt.Errorf("unknown ecdsa signature algorithm")
		}
	case *rsa.PublicKey:
		bitLen := pub.N.BitLen()
		// Reject RSA keys smaller than 2048 bits as insecure
		if bitLen < 2048 {
			return x509.UnknownSignatureAlgorithm, fmt.Errorf("rsa keys smaller than 2048 bits are not allowed")
		}
		switch {
		case bitLen >= 4096:
			return x509.SHA512WithRSA, nil
		case bitLen >= 3072:
			return x509.SHA384WithRSA, nil
		default:
			return x509.SHA256WithRSA, nil
		}
	default:
		return x509.UnknownSignatureAlgorithm, fmt.Errorf("unknown rsa signature algorithm")
	}
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

// GetExtensionValueFromCSR retrieves the raw value of the extension identified
// by oid from csr, searching both Extensions and ExtraExtensions. It returns
// an error if the extension is missing.
func GetExtensionValueFromCSR(csr *x509.CertificateRequest, oid asn1.ObjectIdentifier) ([]byte, error) {
	for _, ext := range append(csr.Extensions, csr.ExtraExtensions...) {
		if ext.Id.Equal(oid) {
			return ext.Value, nil
		}
	}
	return nil, flterrors.ErrExtensionNotFound
}
