package middleware

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// When a TLS client stalls during the handshake it should not block the
// ConnContext function beyond the handshake timeout, and the connection
// should be closed so the accept loop can continue.
func TestTLSClientCertConnContextHandshakeTimeout(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	tlsServerConn := tls.Server(serverConn, &tls.Config{
		Certificates: nil,
		MinVersion:   tls.VersionTLS12,
	})

	log := logrus.New()
	timeout := 100 * time.Millisecond

	connContext := TLSClientCertConnContext(log, timeout)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		connContext(ctx, tlsServerConn)
	}()

	select {
	case <-done:
		// ConnContext returned within the timeout — success.
	case <-time.After(2 * time.Second):
		t.Fatal("ConnContext blocked beyond expected handshake timeout")
	}

	// Verify the stalled server-side connection was closed.
	_ = clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected stalled connection to be closed by server")
	}
}

// When the handshake timeout is zero or negative it should fall back to the
// default timeout rather than allowing an unbounded handshake.
func TestTLSClientCertConnContextDefaultTimeout(t *testing.T) {
	if DefaultTLSHandshakeTimeout <= 0 {
		t.Fatalf("DefaultTLSHandshakeTimeout should be positive, got %v", DefaultTLSHandshakeTimeout)
	}
}

// When the connection is not a *tls.Conn the function should return the
// original context without attempting a handshake.
func TestTLSClientCertConnContextNonTLSConn(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	log := logrus.New()
	connContext := TLSClientCertConnContext(log, time.Second)

	ctx := context.Background()
	resultCtx := connContext(ctx, serverConn)

	if resultCtx != ctx {
		t.Fatal("expected original context for non-TLS connection")
	}
}

func TestValidateClientTlsCert(t *testing.T) {
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: "validate-test"}}

	tests := []struct {
		name        string
		ctx         context.Context
		wantErr     bool
		wantMessage string
	}{
		{
			name: "When the peer has a verified chain it should return the same context without error",
			ctx:  grpcPeerCtxWithCert(cert),
		},
		{
			name:        "When no peer is present it should return Unauthenticated",
			ctx:         context.Background(),
			wantErr:     true,
			wantMessage: "no peer found",
		},
		{
			name: "When peer AuthInfo is not TLS it should return Unauthenticated",
			ctx: peer.NewContext(context.Background(), &peer.Peer{
				AuthInfo: grpcNonTLSAuthInfo{},
			}),
			wantErr:     true,
			wantMessage: "failed to verify client certificate",
		},
		{
			name:        "When peer has no verified chains it should return Unauthenticated",
			ctx:         grpcPeerCtxWithTLSState(tls.ConnectionState{}),
			wantErr:     true,
			wantMessage: "failed to verify client certificate",
		},
		{
			name: "When the first verified chain is empty it should return Unauthenticated",
			ctx: grpcPeerCtxWithTLSState(tls.ConnectionState{
				VerifiedChains: [][]*x509.Certificate{{}, {cert}},
			}),
			wantErr:     true,
			wantMessage: "failed to verify client certificate",
		},
		{
			name: "When the cert is only in PeerCertificates it should return Unauthenticated",
			ctx: grpcPeerCtxWithTLSState(tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{cert},
			}),
			wantErr:     true,
			wantMessage: "failed to verify client certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCtx, err := ValidateClientTlsCert(tt.ctx)

			assert.Equal(t, tt.ctx, gotCtx, "expected the input context to be returned")
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok, "expected gRPC status error")
			assert.Equal(t, codes.Unauthenticated, st.Code())
			assert.Equal(t, tt.wantMessage, st.Message())

			_, helperErr := verifiedClientCert(tt.ctx)
			assert.Equal(t, err, helperErr, "expected the validator to return the helper error")
		})
	}
}

func TestVerifiedClientCert(t *testing.T) {
	verifiedLeaf := &x509.Certificate{Subject: pkix.Name{CommonName: "verified-leaf"}}
	intermediate := &x509.Certificate{Subject: pkix.Name{CommonName: "intermediate"}}
	otherChainLeaf := &x509.Certificate{Subject: pkix.Name{CommonName: "other-chain-leaf"}}
	unverified := &x509.Certificate{Subject: pkix.Name{CommonName: "unverified"}}

	t.Run("When several chains are verified it should return the leaf of the first chain and ignore PeerCertificates", func(t *testing.T) {
		ctx := grpcPeerCtxWithTLSState(tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{unverified},
			VerifiedChains: [][]*x509.Certificate{
				{verifiedLeaf, intermediate},
				{otherChainLeaf},
			},
		})

		got, err := verifiedClientCert(ctx)

		require.NoError(t, err)
		assert.Same(t, verifiedLeaf, got)
	})
}
