package signer

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

func makeCSRWithCN(t *testing.T, cn string) *x509.CertificateRequest {
	t.Helper()
	_, priv, err := fccrypto.NewKeyPair()
	if err != nil {
		t.Fatalf("newKeyPair: %v", err)
	}
	tpl := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn},
	}
	raw, err := x509.CreateCertificateRequest(rand.Reader, tpl, priv.(crypto.Signer))
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csr, err := x509.ParseCertificateRequest(raw)
	if err != nil {
		t.Fatalf("parse csr: %v", err)
	}
	return csr
}

func TestSignerServerSvc(t *testing.T) {
	ca := newMockCA(t)
	cfg := ca.Config()

	type testCase struct {
		name    string
		build   func() (context.Context, SignRequest)
		wantErr bool
	}

	verifyCases := []testCase{
		{
			name: "valid CSR succeeds",
			build: func() (context.Context, SignRequest) {
				csr := makeCSRWithCN(t, "svc-myservice")
				req, err := NewSignRequest(cfg.ServerSvcSignerName, *csr)
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				return context.Background(), req
			},
			wantErr: false,
		},
		{
			name: "CSR with peer certificate is rejected",
			build: func() (context.Context, SignRequest) {
				csr := makeCSRWithCN(t, "svc-myservice")
				req, err := NewSignRequest(cfg.ServerSvcSignerName, *csr)
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				peer := &x509.Certificate{}
				ctx := context.WithValue(context.Background(), consts.TLSPeerCertificateCtxKey, peer)
				return ctx, req
			},
			wantErr: true,
		},
		{
			name: "CSR with empty CN is rejected",
			build: func() (context.Context, SignRequest) {
				csr := makeCSRWithCN(t, "")
				req, err := NewSignRequest(cfg.ServerSvcSignerName, *csr)
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				return context.Background(), req
			},
			wantErr: true,
		},
		{
			name: "CSR with wrong CN prefix is rejected",
			build: func() (context.Context, SignRequest) {
				csr := makeCSRWithCN(t, "notaservice")
				req, err := NewSignRequest(cfg.ServerSvcSignerName, *csr)
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				return context.Background(), req
			},
			wantErr: true,
		},
	}

	signer := NewSignerServerSvc(ca)

	for _, tc := range verifyCases {
		t.Run("Verify/"+tc.name, func(t *testing.T) {
			ctx, req := tc.build()
			err := signer.Verify(ctx, req)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
