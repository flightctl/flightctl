package signer

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/org"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

var (
	NullOrgId            = org.DefaultID
	OIDSignerName        = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 1}
	OIDOrgID             = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 2}
	OIDDeviceFingerprint = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 3}
)

type certOption = func(*x509.Certificate) error

func WithExtension(oid asn1.ObjectIdentifier, value string) certOption {
	return func(cert *x509.Certificate) error {
		encoded, err := asn1.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshal extension for OID %v: %w", oid, err)
		}
		cert.ExtraExtensions = append(cert.ExtraExtensions, pkix.Extension{
			Id:       oid,
			Critical: false,
			Value:    encoded,
		})
		return nil
	}
}

func PeerCertificateFromCtx(ctx context.Context) (*x509.Certificate, error) {
	cert, ok := ctx.Value(consts.TLSPeerCertificateCtxKey).(*x509.Certificate)
	if !ok || cert == nil {
		return nil, fmt.Errorf("peer certificate not found")
	}
	return cert, nil
}

func GetSignerNameExtension(cert *x509.Certificate) (string, error) {
	return fccrypto.GetExtensionValue(cert, OIDSignerName)
}

func DeviceFingerprintFromCN(cfg *ca.Config, commonName string) (string, error) {
	prefix := cfg.DeviceCommonNamePrefix

	if !strings.HasPrefix(commonName, prefix) {
		return "", fmt.Errorf("common name %q missing expected prefix %q", commonName, prefix)
	}

	fingerprint := strings.TrimPrefix(commonName, prefix)
	if len(fingerprint) < 16 {
		return "", fmt.Errorf("fingerprint extracted from CN must be at least %d characters: got %q", 16, fingerprint)
	}

	return fingerprint, nil
}

func CNFromDeviceFingerprint(cfg *ca.Config, fingerprint string) (string, error) {
	if len(fingerprint) < 16 {
		return "", errors.New("device fingerprint must have 16 characters at least")
	}
	if strings.HasPrefix(fingerprint, cfg.DeviceCommonNamePrefix) {
		return fingerprint, nil
	}
	return cfg.DeviceCommonNamePrefix + fingerprint, nil
}

func BootstrapCNFromName(cfg *ca.Config, name string) string {
	commonNames := []string{cfg.ClientBootstrapCommonName}
	for _, cn := range append(commonNames, cfg.ExtraAllowedPrefixes...) {
		if name == cn {
			return name
		}
	}

	prefixes := []string{cfg.ClientBootstrapCommonNamePrefix, cfg.DeviceCommonNamePrefix}
	for _, prefix := range append(prefixes, cfg.ExtraAllowedPrefixes...) {
		if strings.HasPrefix(name, prefix) {
			return name
		}
	}
	return cfg.ClientBootstrapCommonNamePrefix + name
}
