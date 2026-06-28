package service

import (
	"context"
	"testing"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
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
			name:       "When device management signer is used it should be rejected",
			signerName: cfg.DeviceManagementSignerName,
			wantErr:    true,
		},
		{
			name:       "When server svc signer is used it should be rejected",
			signerName: cfg.ServerSvcSignerName,
			wantErr:    true,
		},
		{
			name:       "When enrollment signer is used it should be accepted",
			signerName: cfg.DeviceEnrollmentSignerName,
			wantErr:    false,
		},
		{
			name:       "When device management renewal signer is used it should be accepted",
			signerName: cfg.DeviceManagementRenewalSignerName,
			wantErr:    false,
		},
		{
			name:       "When device svc client signer is used it should be accepted",
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

func TestUpdateCertificateSigningRequestApprovalServerSvc(t *testing.T) {
	cfg := cacfg.NewDefault(t.TempDir())

	superAdminCtx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey,
		identity.NewMappedIdentity("admin", "uid-admin", nil, nil, true, nil))
	nonAdminCtx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey,
		identity.NewMappedIdentity("user", "uid-user", nil, nil, false, nil))

	cases := []struct {
		name       string
		ctx        context.Context
		signerName string
		wantErr    bool
	}{
		{
			name:       "When server svc signer approval is by super-admin it should be accepted",
			ctx:        superAdminCtx,
			signerName: cfg.ServerSvcSignerName,
			wantErr:    false,
		},
		{
			name:       "When server svc signer approval is by non-super-admin it should be rejected",
			ctx:        nonAdminCtx,
			signerName: cfg.ServerSvcSignerName,
			wantErr:    true,
		},
		{
			name:       "When server svc signer approval is without identity it should be rejected",
			ctx:        context.Background(),
			signerName: cfg.ServerSvcSignerName,
			wantErr:    true,
		},
		{
			name:       "When server svc signer denial is by non-super-admin it should be rejected",
			ctx:        nonAdminCtx,
			signerName: cfg.ServerSvcSignerName,
			wantErr:    true,
		},
		{
			name:       "When non-server-svc signer approval is by non-super-admin it should be accepted",
			ctx:        nonAdminCtx,
			signerName: cfg.DeviceEnrollmentSignerName,
			wantErr:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkServerSvcApprovalPrivilege(tc.ctx, tc.signerName, cfg.ServerSvcSignerName)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
