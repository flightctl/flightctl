package middleware

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"testing"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// grpcPeerCtxWithTLSState builds a gRPC peer context carrying the supplied TLS
// connection state.
func grpcPeerCtxWithTLSState(state tls.ConnectionState) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: state},
	})
}

// grpcPeerCtxWithCert builds a gRPC peer context whose TLS VerifiedChains
// contain the supplied certificate, simulating a successful mTLS handshake.
func grpcPeerCtxWithCert(cert *x509.Certificate) context.Context {
	return grpcPeerCtxWithTLSState(tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	})
}

// grpcCertWithOrgValue returns a test certificate whose OIDOrgID extension
// carries the given string, using the same encoding the CA uses when issuing
// device certs.
func grpcCertWithOrgValue(t *testing.T, commonName, value string) *x509.Certificate {
	t.Helper()
	encoded, err := asn1.Marshal(value)
	require.NoError(t, err)
	return &x509.Certificate{
		Subject: pkix.Name{CommonName: commonName},
		Extensions: []pkix.Extension{{
			Id:       signer.OIDOrgID,
			Critical: false,
			Value:    encoded,
		}},
	}
}

// grpcCertWithOrgID returns a test certificate carrying the given org ID in the
// OIDOrgID extension.
func grpcCertWithOrgID(t *testing.T, id uuid.UUID) *x509.Certificate {
	t.Helper()
	return grpcCertWithOrgValue(t, "grpc-test", id.String())
}

// grpcNonTLSAuthInfo is a credentials.AuthInfo that is not TLS.
type grpcNonTLSAuthInfo struct{}

func (grpcNonTLSAuthInfo) AuthType() string { return "non-tls" }

func TestGrpcAuthMiddleware(t *testing.T) {
	nonDefaultOrg := uuid.New()
	unverifiedOrg := uuid.New()

	tests := []struct {
		name      string
		ctx       context.Context
		wantCode  codes.Code
		wantOrgID uuid.UUID
		wantErr   bool
	}{
		{
			name:      "When cert has a non-default org ID it should set that org in context",
			ctx:       grpcPeerCtxWithCert(grpcCertWithOrgID(t, nonDefaultOrg)),
			wantOrgID: nonDefaultOrg,
		},
		{
			name:      "When cert has the default org ID it should set the default org in context",
			ctx:       grpcPeerCtxWithCert(grpcCertWithOrgID(t, org.DefaultID)),
			wantOrgID: org.DefaultID,
		},
		{
			name: "When cert has no org extension it should fall back to the default org",
			ctx: grpcPeerCtxWithCert(&x509.Certificate{
				Subject: pkix.Name{CommonName: "no-extension"},
			}),
			wantOrgID: org.DefaultID,
		},
		{
			name:     "When no peer is present it should return Unauthenticated",
			ctx:      context.Background(),
			wantCode: codes.Unauthenticated,
			wantErr:  true,
		},
		{
			name: "When peer has no verified chains it should return Unauthenticated",
			ctx: peer.NewContext(context.Background(), &peer.Peer{
				AuthInfo: credentials.TLSInfo{
					State: tls.ConnectionState{
						VerifiedChains: nil,
					},
				},
			}),
			wantCode: codes.Unauthenticated,
			wantErr:  true,
		},
		{
			name:     "When cert has a malformed org ID extension it should return Unauthenticated",
			ctx:      grpcPeerCtxWithCert(grpcCertWithOrgValue(t, "bad-org", "not-a-uuid")),
			wantCode: codes.Unauthenticated,
			wantErr:  true,
		},
		{
			name: "When the cert is only in PeerCertificates it should return Unauthenticated",
			ctx: grpcPeerCtxWithTLSState(tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{grpcCertWithOrgID(t, nonDefaultOrg)},
			}),
			wantCode: codes.Unauthenticated,
			wantErr:  true,
		},
		{
			name: "When peer AuthInfo is not TLS it should return Unauthenticated",
			ctx: peer.NewContext(context.Background(), &peer.Peer{
				AuthInfo: grpcNonTLSAuthInfo{},
			}),
			wantCode: codes.Unauthenticated,
			wantErr:  true,
		},
		{
			name: "When the first verified chain is empty it should return Unauthenticated",
			ctx: grpcPeerCtxWithTLSState(tls.ConnectionState{
				VerifiedChains: [][]*x509.Certificate{{}, {grpcCertWithOrgID(t, nonDefaultOrg)}},
			}),
			wantCode: codes.Unauthenticated,
			wantErr:  true,
		},
		{
			name: "When the verified leaf differs from PeerCertificates it should use the verified leaf org",
			ctx: grpcPeerCtxWithTLSState(tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{grpcCertWithOrgID(t, unverifiedOrg)},
				VerifiedChains:   [][]*x509.Certificate{{grpcCertWithOrgID(t, nonDefaultOrg)}},
			}),
			wantOrgID: nonDefaultOrg,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := GrpcAuthMiddleware(tt.ctx)

			if tt.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tt.wantCode, st.Code())
				return
			}

			require.NoError(t, err)
			gotOrgID, ok := util.GetOrgIdFromContext(ctx)
			require.True(t, ok, "expected organization ID in context")
			assert.Equal(t, tt.wantOrgID, gotOrgID)
		})
	}
}
