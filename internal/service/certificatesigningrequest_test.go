package service

import (
	"testing"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
)

func TestValidateAllowedSignersForCSRService(t *testing.T) {
	cfg := cacfg.NewDefault(t.TempDir())
	ca, _, err := crypto.EnsureCA(cfg)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	h := &ServiceHandler{ca: ca}

	cases := []struct {
		name       string
		signerName string
		wantErr    bool
	}{
		{
			name:       "device management signer is blocked",
			signerName: cfg.DeviceManagementSignerName,
			wantErr:    true,
		},
		{
			name:       "server svc signer is blocked",
			signerName: cfg.ServerSvcSignerName,
			wantErr:    true,
		},
		{
			name:       "enrollment signer is allowed",
			signerName: cfg.DeviceEnrollmentSignerName,
			wantErr:    false,
		},
		{
			name:       "device management renewal signer is allowed",
			signerName: cfg.DeviceManagementRenewalSignerName,
			wantErr:    false,
		},
		{
			name:       "device svc client signer is allowed",
			signerName: cfg.DeviceSvcClientSignerName,
			wantErr:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csr := &domain.CertificateSigningRequest{
				Spec: domain.CertificateSigningRequestSpec{
					SignerName: tc.signerName,
				},
			}
			err := h.validateAllowedSignersForCSRService(csr)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
