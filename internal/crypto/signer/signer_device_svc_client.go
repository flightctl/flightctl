package signer

import (
	"context"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

const signerDeviceSvcClientExpiryDays int32 = 7

type SignerDeviceSvcClient struct {
	name string
	ca   CA
}

func NewSignerDeviceSvcClient(CAClient CA) Signer {
	cfg := CAClient.Config()
	return &SignerDeviceSvcClient{name: cfg.DeviceSvcClientSignerName, ca: CAClient}
}

func (s *SignerDeviceSvcClient) Name() string {
	return s.name
}

func (s *SignerDeviceSvcClient) Verify(ctx context.Context, request api.CertificateSigningRequest) error {
	cfg := s.ca.Config()

	if s := s.ca.SignerFromCtx(ctx); s == nil || s.Name() != cfg.DeviceEnrollmentSignerName {
		return fmt.Errorf("unexpected client certificate signer: expected %q, got %q", cfg.DeviceEnrollmentSignerName, s.Name())
	}

	peerCertificate, err := PeerCertificateFromCtx(ctx)
	if err != nil {
		return fmt.Errorf("failed to get peer certificate from context: %w", err)
	}

	fingerprint, err := DeviceFingerprintFromCN(cfg, peerCertificate.Subject.CommonName)
	if err != nil {
		return fmt.Errorf("failed to extract device fingerprint from peer certificate CN: %w", err)
	}

	parsedCSR, err := fccrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return fmt.Errorf("failed to parse CSR: %w", err)
	}

	if !strings.HasSuffix(parsedCSR.Subject.CommonName, fmt.Sprintf("-%s", fingerprint)) {
		return fmt.Errorf("CSR CommonName %q does not end with device fingerprint suffix -%s", parsedCSR.Subject.CommonName, fingerprint)
	}

	if *request.Metadata.Name != parsedCSR.Subject.CommonName {
		return fmt.Errorf("CSR metadata.name %q does not match CSR subject CommonName %q", *request.Metadata.Name, parsedCSR.Subject.CommonName)
	}

	return nil
}

func (s *SignerDeviceSvcClient) Sign(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
	cert, err := fccrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, err
	}

	cn := strings.Split(cert.Subject.CommonName, "-")
	fingerprint := cn[len(cn)-1]

	expirySeconds := signerDeviceSvcClientExpiryDays * 24 * 60 * 60
	if request.Spec.ExpirationSeconds != nil && *request.Spec.ExpirationSeconds < expirySeconds {
		expirySeconds = *request.Spec.ExpirationSeconds
	}

	return s.ca.IssueRequestedClientCertificate(
		ctx,
		cert,
		int(expirySeconds),
		WithExtension(OIDOrgID, NullOrgId.String()),
		WithExtension(OIDDeviceFingerprint, fingerprint),
	)
}
