package signer

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto/utils"
	"github.com/flightctl/flightctl/internal/flterrors"
)

type CertOption = func(*x509.Certificate) error

type Signer interface {
	Name() string
	Verify(ctx context.Context, csr api.CertificateSigningRequest) error
	Sign(request api.CertificateSigningRequest) ([]byte, error)
}

type CA interface {
	Config() *ca.Config
	IssueRequestedClientCertificate(csr *x509.CertificateRequest, expirySeconds int, opts ...CertOption) ([]byte, error)
}

type SignerDeviceSvcClient struct {
	name string
	ca   CA
}

func NewSignerDeviceSvcClient(CAClient CA) *SignerDeviceSvcClient {
	cfg := CAClient.Config()
	return &SignerDeviceSvcClient{name: cfg.DeviceSvcClientSignerName, ca: CAClient}
}

func (s *SignerDeviceSvcClient) Name() string {
	return s.name
}

func (s *SignerDeviceSvcClient) Verify(ctx context.Context, request api.CertificateSigningRequest) error {
	cfg := s.ca.Config()

	cn, ok := ctx.Value(consts.TLSCommonNameCtxKey).(string)
	if !ok || cn == "" {
		return fmt.Errorf("missing TLS common name")
	}

	if errs := request.Validate(); len(errs) > 0 {
		return errors.Join(errs...)
	}

	fingerprint, err := s.deviceFingerprintFromCN(cn)
	if err != nil {
		return fmt.Errorf("invalid device identity")
	}

	if request.Spec.SignerName != s.name {
		return fmt.Errorf("invalid signer name")
	}

	if request.Spec.ExpirationSeconds == nil {
		return fmt.Errorf("invalid CSR expiration")
	}

	parsedCSR, err := utils.ParseCSR(request.Spec.Request)
	if err != nil {
		return fmt.Errorf("invalid CSR data")
	}

	if err := parsedCSR.CheckSignature(); err != nil {
		return fmt.Errorf("invalid CSR signature")
	}

	if strings.HasPrefix(parsedCSR.Subject.CommonName, cfg.DeviceCommonNamePrefix) {
		return fmt.Errorf("invalid CN - denied attempt to renew other entity certificate")
	}

	if !strings.HasSuffix(parsedCSR.Subject.CommonName, fmt.Sprintf("-%s", fingerprint)) {
		return fmt.Errorf("invalid CN")
	}

	if *request.Metadata.Name != parsedCSR.Subject.CommonName {
		return fmt.Errorf("CSR name and subject CommonName mismatch")
	}

	return nil
}

var OIDDeviceFingerprint = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 1}

func (s *SignerDeviceSvcClient) Sign(request api.CertificateSigningRequest) ([]byte, error) {
	cert, err := utils.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, fmt.Errorf("invalid CSR data")
	}

	if err := cert.CheckSignature(); err != nil {
		return nil, fmt.Errorf("invalid CSR signature")
	}

	cn := strings.Split(cert.Subject.CommonName, "-")
	fingerprint := cn[len(cn)-1]

	encoded, err := asn1.Marshal(fingerprint)
	if err != nil {
		return nil, fmt.Errorf("marshal extension for OID %v: %w", OIDDeviceFingerprint, err)
	}

	cert.ExtraExtensions = append(cert.ExtraExtensions, pkix.Extension{
		Id:       OIDDeviceFingerprint,
		Critical: false,
		Value:    encoded,
	})

	return s.ca.IssueRequestedClientCertificate(
		cert,
		int(*request.Spec.ExpirationSeconds),
	)
}

func (s *SignerDeviceSvcClient) deviceFingerprintFromCN(commonName string) (string, error) {
	prefix := s.ca.Config().DeviceCommonNamePrefix

	if !strings.HasPrefix(commonName, prefix) {
		return "", fmt.Errorf("common name %q missing expected prefix %q", commonName, prefix)
	}

	fingerprint := strings.TrimPrefix(commonName, prefix)
	if len(fingerprint) < 16 {
		return "", fmt.Errorf("fingerprint extracted from CN must be at least %d characters: got %q", 16, fingerprint)
	}

	return fingerprint, nil
}

type SignerEnrollment struct {
	SignerLegacy
}

func NewSignerEnrollment(CAClient CA) *SignerLegacy {
	cfg := CAClient.Config()
	return &SignerLegacy{name: cfg.ClientBootstrapSignerName, ca: CAClient}
}

type SignerLegacy struct {
	name string
	ca   CA
}

func NewSignerLegacy(CAClient CA, name string) *SignerLegacy {
	return &SignerLegacy{name: name, ca: CAClient}
}

func (s *SignerLegacy) Name() string {
	return s.name
}

func (s *SignerLegacy) Verify(ctx context.Context, request api.CertificateSigningRequest) error {
	// Crypto validation
	cn, ok := ctx.Value(consts.TLSCommonNameCtxKey).(string)

	// Note - if auth is disabled and there is no mTLS handshake we get ok == False.
	// We cannot check anything in that case.

	if ok {
		cfg := s.ca.Config()
		if cfg == nil {
			return fmt.Errorf("no CA valid config")
		}

		if request.Spec.SignerName != s.name {
			if request.Metadata.Name == nil {
				return errors.New("invalid csr record - no name in metadata")
			}
			if cn != bootstrapCNFromName(cfg, *request.Metadata.Name) {
				return errors.New("denied attempt to renew other entity certificate")
			}
		}
	}
	return nil
}

func (s *SignerLegacy) Sign(request api.CertificateSigningRequest) ([]byte, error) {
	cfg := s.ca.Config()

	if request.Status.Certificate != nil && len(*request.Status.Certificate) > 0 {
		return *request.Status.Certificate, nil
	}

	csr, err := utils.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, err
	}

	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrSignature, err)
	}

	// the CN will need the enrollment prefix applied;
	// if the certificate is being renewed, the name will have an existing prefix.
	// we do not touch in this case.

	u := csr.Subject.CommonName

	// Once we move all prefixes/name formation to the client this can become a simple
	// comparison of u and *request.Metadata.Name

	if bootstrapCNFromName(cfg, u) != bootstrapCNFromName(cfg, *request.Metadata.Name) {
		return nil, fmt.Errorf("%w - CN %s Metadata %s mismatch", flterrors.ErrSignCert, u, *request.Metadata.Name)
	}

	csr.Subject.CommonName = bootstrapCNFromName(cfg, u)

	var expiry int32 = 60 * 60 * 24 * 7 // 7 days
	if request.Spec.ExpirationSeconds != nil {
		expiry = *request.Spec.ExpirationSeconds
	}

	certData, err := s.ca.IssueRequestedClientCertificate(csr, int(expiry))
	if err != nil {
		return nil, err
	}

	return certData, nil
}

func bootstrapCNFromName(cfg *ca.Config, name string) string {
	if cfg == nil {
		return ""
	}

	base := []string{cfg.ClientBootstrapCommonNamePrefix, cfg.DeviceCommonNamePrefix}
	for _, prefix := range append(base, cfg.ExtraAllowedPrefixes...) {
		if strings.HasPrefix(name, prefix) {
			return name
		}
	}
	return cfg.ClientBootstrapCommonNamePrefix + name
}
