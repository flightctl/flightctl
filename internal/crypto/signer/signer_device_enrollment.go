package signer

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
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

func (s *SignerDeviceEnrollment) Verify(ctx context.Context, request api.CertificateSigningRequest) error {
	cfg := s.ca.Config()

	signer := s.ca.SignerFromCtx(ctx)

	got := "<nil>"
	if signer != nil {
		got = signer.Name()
	}

	if signer == nil || signer.Name() != cfg.ClientBootstrapSignerName {
		return fmt.Errorf("unexpected client certificate signer: expected %q, got %q", cfg.ClientBootstrapSignerName, got)
	}

	return nil
}

func (s *SignerDeviceEnrollment) Sign(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
	cfg := s.ca.Config()

	if request.Metadata.Name == nil {
		return nil, fmt.Errorf("request is missing metadata.name")
	}

	csr, err := fccrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, fmt.Errorf("error parsing CSR: %w", err)
	}

	supplied, err := CNFromDeviceFingerprint(cfg, csr.Subject.CommonName)
	if err != nil {
		return nil, fmt.Errorf("invalid CN supplied in CSR: %w", err)
	}

	desired, err := CNFromDeviceFingerprint(cfg, *request.Metadata.Name)
	if err != nil {
		return nil, fmt.Errorf("error setting CN in CSR: %w", err)
	}

	if desired != supplied {
		return nil, fmt.Errorf("approveAndSignEnrollmentRequest: attempt to supply a fake CN, possible identity theft, csr: %s, metadata %s", supplied, desired)
	}
	csr.Subject.CommonName = desired

	expirySeconds := signerDeviceEnrollmentExpiryDays * 24 * 60 * 60
	if request.Spec.ExpirationSeconds != nil && *request.Spec.ExpirationSeconds < expirySeconds {
		expirySeconds = *request.Spec.ExpirationSeconds
	}

	return s.ca.IssueRequestedClientCertificate(
		ctx,
		csr,
		int(expirySeconds),
		WithExtension(OIDOrgID, NullOrgId.String()),
		WithExtension(OIDDeviceFingerprint, csr.Subject.CommonName),
	)
}
