package signer

import (
	"context"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
)

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

	if s := s.ca.GetSignerFromCtx(ctx); s == nil || s.Name() != cfg.DeviceEnrollmentSignerName {
		return fmt.Errorf("unexpected client certificate signer: expected %q, got %q", cfg.DeviceEnrollmentSignerName, s.Name())
	}

	cn, ok := ctx.Value(consts.TLSCommonNameCtxKey).(string)
	if !ok || cn == "" {
		return fmt.Errorf("missing TLS common name")
	}

	fingerprint, err := deviceFingerprintFromCN(cfg, cn)
	if err != nil {
		return fmt.Errorf("invalid device identity")
	}

	if request.Spec.ExpirationSeconds == nil {
		return fmt.Errorf("invalid CSR expiration")
	}

	parsedCSR, err := fcrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return fmt.Errorf("invalid CSR data")
	}

	if err := parsedCSR.CheckSignature(); err != nil {
		return fmt.Errorf("invalid CSR signature")
	}

	if !strings.HasSuffix(parsedCSR.Subject.CommonName, fmt.Sprintf("-%s", fingerprint)) {
		return fmt.Errorf("invalid CN")
	}

	if *request.Metadata.Name != parsedCSR.Subject.CommonName {
		return fmt.Errorf("CSR name and subject CommonName mismatch")
	}

	return nil
}

func (s *SignerDeviceSvcClient) Sign(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
	cert, err := fcrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, fmt.Errorf("invalid CSR data")
	}

	if err := cert.CheckSignature(); err != nil {
		return nil, fmt.Errorf("invalid CSR signature")
	}

	cn := strings.Split(cert.Subject.CommonName, "-")
	fingerprint := cn[len(cn)-1]

	return s.ca.IssueRequestedClientCertificate(
		ctx,
		cert,
		int(*request.Spec.ExpirationSeconds),
		WithExtension(OIDDeviceFingerprint, fingerprint),
	)
}
