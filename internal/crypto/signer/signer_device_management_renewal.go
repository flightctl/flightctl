package signer

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
)

type SignerDeviceManagementRenewal struct {
	name string
	ca   CA
}

func NewSignerDeviceManagementRenewal(CAClient CA) Signer {
	cfg := CAClient.Config()
	return &SignerDeviceManagementRenewal{
		name: cfg.DeviceManagementRenewalSignerName,
		ca:   CAClient,
	}
}

func (s *SignerDeviceManagementRenewal) RestrictedPrefix() string {
	return s.ca.Config().DeviceCommonNamePrefix
}

func (s *SignerDeviceManagementRenewal) Name() string {
	return s.name
}

func (s *SignerDeviceManagementRenewal) Verify(ctx context.Context, request SignRequest) error {
	cfg := s.ca.Config()

	peer, err := PeerCertificateFromCtx(ctx)
	if err != nil {
		return fmt.Errorf("unable to retrieve peer certificate: %w", err)
	}

	peerSigner := s.ca.PeerCertificateSignerFromCtx(ctx)
	if peerSigner == nil {
		return fmt.Errorf("could not determine peer certificate signer")
	}

	if !IsDeviceManagementClientCertSigner(cfg, peerSigner) {
		return fmt.Errorf(
			"unexpected client certificate signer: expected %q or %q, got %q",
			cfg.DeviceManagementSignerName,
			cfg.DeviceManagementRenewalSignerName,
			peerSigner.Name(),
		)
	}

	x509CSR := request.X509()
	supplied, err := CNFromDeviceFingerprint(cfg, x509CSR.Subject.CommonName)
	if err != nil {
		return fmt.Errorf("invalid CN supplied in CSR: %w", err)
	}

	peerCN, err := CNFromDeviceFingerprint(cfg, peer.Subject.CommonName)
	if err != nil {
		return fmt.Errorf("invalid peer certificate CN: %w", err)
	}

	if peerCN != supplied {
		return fmt.Errorf("csr CN mismatch: csr=%q peer=%q", supplied, peerCN)
	}

	return nil
}

func (s *SignerDeviceManagementRenewal) Sign(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
	cfg := s.ca.Config()

	x509CSR := request.X509()

	cn, err := CNFromDeviceFingerprint(cfg, x509CSR.Subject.CommonName)
	if err != nil {
		return nil, fmt.Errorf("invalid CN supplied in CSR: %w", err)
	}
	x509CSR.Subject.CommonName = cn

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
