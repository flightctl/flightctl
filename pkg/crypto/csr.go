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
	"errors"
	"fmt"
	"net"
	urlpkg "net/url"

	"github.com/flightctl/flightctl/internal/flterrors"
)

// CSROption allows callers to customize the x509.CertificateRequest template
// before the CSR is created.
type CSROption func(*x509.CertificateRequest) error

// WithSubject overrides the Subject for the CSR.
func WithSubject(subject pkix.Name) CSROption {
	return func(t *x509.CertificateRequest) error {
		t.Subject = subject
		return nil
	}
}

// WithDNSNames sets one or more DNS SANs on the CSR.
func WithDNSNames(names ...string) CSROption {
	return func(t *x509.CertificateRequest) error {
		t.DNSNames = append([]string{}, names...)
		return nil
	}
}

// WithIPAddresses sets one or more IP SANs on the CSR.
func WithIPAddresses(ips ...net.IP) CSROption {
	return func(t *x509.CertificateRequest) error {
		t.IPAddresses = append([]net.IP{}, ips...)
		return nil
	}
}

// WithURIs sets one or more URI SANs on the CSR.
func WithURIs(uris ...*urlpkg.URL) CSROption {
	return func(t *x509.CertificateRequest) error {
		t.URIs = append([]*urlpkg.URL{}, uris...)
		return nil
	}
}

// WithExtraExtension adds a non-critical extra extension (value ASN.1-encoded string).
func WithExtraExtension(oid asn1.ObjectIdentifier, value string) CSROption {
	return func(t *x509.CertificateRequest) error {
		enc, err := asn1.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshal extension for OID %v: %w", oid, err)
		}
		t.ExtraExtensions = append(t.ExtraExtensions, pkix.Extension{Id: oid, Critical: false, Value: enc})
		return nil
	}
}

// MakeCSR creates a PEM-encoded CSR using the provided private key,
func MakeCSR(privateKey crypto.Signer, subjectName string, opts ...CSROption) ([]byte, error) {
	algo, err := selectSignatureAlgorithm(privateKey)
	if err != nil {
		return nil, fmt.Errorf("selecting signature algorithm: %w", err)
	}

	template := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: subjectName},
		SignatureAlgorithm: algo,
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(template); err != nil {
			return nil, err
		}
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

// GetCSRExtensionValueAsStr retrieves a specific extension from a CSR as a string.
func GetCSRExtensionValueAsStr(csr *x509.CertificateRequest, oid asn1.ObjectIdentifier) (string, error) {
	for _, ext := range append(csr.Extensions, csr.ExtraExtensions...) {
		if ext.Id.Equal(oid) {
			var s string
			if _, err := asn1.Unmarshal(ext.Value, &s); err == nil {
				return s, nil
			}
			return "", fmt.Errorf("unmarshalling extension %v", oid)
		}
	}
	return "", flterrors.ErrExtensionNotFound
}

func ValidateX509CSR(c *x509.CertificateRequest) error {
	if err := c.CheckSignature(); err != nil {
		return errors.Join(flterrors.ErrCSRInvalid, err)
	}
	return nil
}
