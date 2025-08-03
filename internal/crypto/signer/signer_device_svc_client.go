package signer

import (
	"context"
	"fmt"
	"strings"
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

func (s *SignerDeviceSvcClient) Verify(ctx context.Context, request *Request) error {
	cfg := s.ca.Config()

	signer := s.ca.PeerCertificateSignerFromCtx(ctx)

	got := "<nil>"
	if signer != nil {
		got = signer.Name()
	}

	if signer == nil || signer.Name() != cfg.DeviceEnrollmentSignerName {
		return fmt.Errorf("unexpected client certificate signer: expected %q, got %q", cfg.DeviceEnrollmentSignerName, got)
	}

	peerCertificate, err := PeerCertificateFromCtx(ctx)
	if err != nil {
		return fmt.Errorf("failed to get peer certificate from context: %w", err)
	}

	fingerprint, err := DeviceFingerprintFromCN(cfg, peerCertificate.Subject.CommonName)
	if err != nil {
		return fmt.Errorf("failed to extract device fingerprint from peer certificate CN: %w", err)
	}

	x509CSR := request.X509()
	if !strings.HasSuffix(x509CSR.Subject.CommonName, fmt.Sprintf("-%s", fingerprint)) {
		return fmt.Errorf("CSR CommonName %q does not end with device fingerprint suffix -%s", x509CSR.Subject.CommonName, fingerprint)
	}

	return nil
}

func (s *SignerDeviceSvcClient) Sign(ctx context.Context, request *Request) ([]byte, error) {
	x509CSR := request.X509()
	lastHyphen := strings.LastIndex(x509CSR.Subject.CommonName, "-")
	if lastHyphen == -1 {
		return nil, fmt.Errorf("invalid CN format: no hyphen found in %q", x509CSR.Subject.CommonName)
	}
	fingerprint := x509CSR.Subject.CommonName[lastHyphen+1:]

	expirySeconds := signerDeviceSvcClientExpiryDays * 24 * 60 * 60
	if request.API.Spec.ExpirationSeconds != nil && *request.API.Spec.ExpirationSeconds < expirySeconds {
		expirySeconds = *request.API.Spec.ExpirationSeconds
	}

	return s.ca.IssueRequestedClientCertificate(
		ctx,
		&x509CSR,
		int(expirySeconds),
		WithExtension(OIDOrgID, NullOrgId.String()),
		WithExtension(OIDDeviceFingerprint, fingerprint),
	)
}
