package signer

import (
	"context"
	"crypto/x509"
	"fmt"
)

const signerDeviceEnrollmentExpiryDays int32 = 365

type SignerDeviceEnrollment struct {
	name string
	ca   CA
}

func NewSignerDeviceEnrollment(CAClient CA) Signer {
	cfg := CAClient.Config()
	return &SignerDeviceEnrollment{name: cfg.DeviceEnrollmentSignerName, ca: CAClient}
}

func (s *SignerDeviceEnrollment) RestrictedPrefix() string {
	return s.ca.Config().DeviceCommonNamePrefix
}

func (s *SignerDeviceEnrollment) Name() string {
	return s.name
}

func (s *SignerDeviceEnrollment) Verify(ctx context.Context, request SignRequest) error {
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
		if signer == nil || signer.Name() != cfg.ClientBootstrapSignerName {
			return fmt.Errorf("unexpected client certificate signer: expected %q, got %q", cfg.ClientBootstrapSignerName, got)
		}
	}
	return nil
}

func (s *SignerDeviceEnrollment) Sign(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
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

	expirySeconds := signerDeviceEnrollmentExpiryDays * 24 * 60 * 60
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
