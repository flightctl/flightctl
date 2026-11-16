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

// grpcPeerCtxWithCert builds a gRPC peer context whose TLS VerifiedChains
// contain the supplied certificate, simulating a successful mTLS handshake.
func grpcPeerCtxWithCert(cert *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{cert},
				VerifiedChains:   [][]*x509.Certificate{{cert}},
			},
		},
	})
}

// grpcCertWithOrgID returns a test certificate carrying the given org ID in the
// OIDOrgID extension—the same encoding the CA uses when issuing device certs.
func grpcCertWithOrgID(id uuid.UUID) *x509.Certificate {
	encoded, _ := asn1.Marshal(id.String())
	return &x509.Certificate{
		Subject: pkix.Name{CommonName: "grpc-test"},
		Extensions: []pkix.Extension{{
			Id:       signer.OIDOrgID,
			Critical: false,
			Value:    encoded,
		}},
	}
}

func TestGrpcAuthMiddleware(t *testing.T) {
	nonDefaultOrg := uuid.New()

	tests := []struct {
		name      string
		ctx       context.Context
		wantCode  codes.Code
		wantOrgID uuid.UUID
		wantErr   bool
	}{
		{
			name:      "When cert has a non-default org ID it should set that org in context",
			ctx:       grpcPeerCtxWithCert(grpcCertWithOrgID(nonDefaultOrg)),
			wantOrgID: nonDefaultOrg,
		},
		{
			name:      "When cert has the default org ID it should set the default org in context",
			ctx:       grpcPeerCtxWithCert(grpcCertWithOrgID(org.DefaultID)),
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
			name: "When cert has a malformed org ID extension it should return Unauthenticated",
			ctx: grpcPeerCtxWithCert(func() *x509.Certificate {
				// Encode a string that is not a valid UUID.
				encoded, _ := asn1.Marshal("not-a-uuid")
				return &x509.Certificate{
					Subject: pkix.Name{CommonName: "bad-org"},
					Extensions: []pkix.Extension{{
						Id:       signer.OIDOrgID,
						Critical: false,
						Value:    encoded,
					}},
				}
			}()),
			wantCode: codes.Unauthenticated,
			wantErr:  true,
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
