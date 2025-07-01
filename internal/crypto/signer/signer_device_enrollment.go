package signer

import (
	"context"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
)

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
	/*
		cfg := s.ca.Config()

		signer, ok := ctx.Value(consts.TLSSignerNameCtxKey).(string)
		if !ok {
			return fmt.Errorf("no issuer")
		}
		if signer != cfg.ClientBootstrapSignerName {
			return fmt.Errorf("bad issuer")
		}
	*/
	if request.Metadata.Name == nil {
		return fmt.Errorf("request is missing metadata.name")
	}

	cert, err := fcrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return fmt.Errorf("invalid CSR data")
	}

	if err := cert.CheckSignature(); err != nil {
		return fmt.Errorf("invalid CSR signature")
	}

	if len(cert.Subject.CommonName) < 16 {
		return fmt.Errorf("device fingerprint must have 16 characters at least")
	}

	return nil

}

func (s *SignerDeviceEnrollment) Sign(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
	cert, err := fcrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, fmt.Errorf("invalid CSR data")
	}

	if err := cert.CheckSignature(); err != nil {
		return nil, fmt.Errorf("invalid CSR signature")
	}

	commonName := cert.Subject.CommonName
	if !strings.HasPrefix(commonName, s.RestrictedPrefix()) {
		commonName = s.RestrictedPrefix() + commonName
	}

	cert.Subject.CommonName = commonName
	return s.ca.IssueRequestedClientCertificate(
		ctx,
		cert,
		int(*request.Spec.ExpirationSeconds),
		WithExtension(OIDDeviceFingerprint, cert.Subject.CommonName),
	)
}
