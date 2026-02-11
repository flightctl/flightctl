package signer

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
)

const signerDeviceManagementExpiryDays int32 = 365

type SignerDeviceManagement struct {
	name string
	ca   CA
}

func NewSignerDeviceManagement(CAClient CA) Signer {
	cfg := CAClient.Config()
	return &SignerDeviceManagement{name: cfg.DeviceManagementSignerName, ca: CAClient}
}

func (s *SignerDeviceManagement) RestrictedPrefix() string {
	return s.ca.Config().DeviceCommonNamePrefix
}

func (s *SignerDeviceManagement) Name() string {
	return s.name
}

func (s *SignerDeviceManagement) Verify(ctx context.Context, request SignRequest) error {
	cfg := s.ca.Config()

	// Check if the client presented a peer certificate during the mTLS handshake.
	// If no peer certificate was presented, we allow the request to proceed without additional signer checks.
	if _, err := PeerCertificateFromCtx(ctx); err == nil {
		signer := s.ca.PeerCertificateSignerFromCtx(ctx)

		got := "<nil>"
		if signer != nil {
			got = signer.Name()
		}

		// Enforce that if a client certificate was presented, it must be signed by the expected bootstrap signer.
		// This ensures only bootstrap client certificates can be used to perform device enrollment.
		if signer == nil || signer.Name() != cfg.DeviceEnrollmentSignerName {
			return fmt.Errorf("unexpected client certificate signer: expected %q, got %q", cfg.DeviceEnrollmentSignerName, got)
		}
	}
	return nil
}

func (s *SignerDeviceManagement) Sign(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
	cfg := s.ca.Config()

	if request.ResourceName() == nil {
		return nil, fmt.Errorf("request is missing metadata.name")
	}

	// Parse the CSR (for TCG CSRs, the service layer provides the embedded standard CSR)
	x509CSR := request.X509()
	supplied, err := CNFromDeviceFingerprint(cfg, x509CSR.Subject.CommonName)
	if err != nil {
		return nil, fmt.Errorf("invalid CN supplied in CSR: %w", err)
	}

	desired, err := CNFromDeviceFingerprint(cfg, *request.ResourceName())
	if err != nil {
		return nil, fmt.Errorf("error setting CN in CSR: %w", err)
	}

	if desired != supplied {
		return nil, fmt.Errorf("attempt to supply a fake CN, possible identity theft, csr: %s, metadata %s", supplied, desired)
	}

	x509CSR.Subject.CommonName = desired

	// Default expiry (can be overridden in tests via env var).
	expirySeconds := signerDeviceManagementExpiryDays * 24 * 60 * 60
	if v := os.Getenv("FLIGHTCTL_TEST_MGMT_CERT_EXPIRY_SECONDS"); v != "" {
		if seconds, err := strconv.ParseInt(v, 10, 32); err == nil && seconds > 0 {
			expirySeconds = int32(seconds)
		}
	}

	// If request specifies a smaller expiry, honor it.
	if request.ExpirationSeconds() != nil && *request.ExpirationSeconds() < expirySeconds {
		expirySeconds = *request.ExpirationSeconds()
	}

	return s.ca.IssueRequestedClientCertificate(
		ctx,
		&x509CSR,
		int(expirySeconds),
		WithExtension(OIDDeviceFingerprint, x509CSR.Subject.CommonName),
	)
}
