package middleware

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func NewHTTPServer(router http.Handler, log logrus.FieldLogger, address string, cfg *config.Config) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           router,
		ReadTimeout:       time.Duration(cfg.Service.HttpReadTimeout),
		ReadHeaderTimeout: time.Duration(cfg.Service.HttpReadHeaderTimeout),
		WriteTimeout:      time.Duration(cfg.Service.HttpWriteTimeout),
		IdleTimeout:       time.Duration(cfg.Service.HttpIdleTimeout),
		MaxHeaderBytes:    cfg.Service.HttpMaxHeaderBytes,
	}
}

// DefaultTLSHandshakeTimeout bounds the TLS handshake in ConnContext to
// prevent a stalled client from blocking the accept loop indefinitely.
const DefaultTLSHandshakeTimeout = 30 * time.Second

func NewHTTPServerWithTLSContext(router http.Handler, log logrus.FieldLogger, address string, cfg *config.Config) *http.Server {
	server := NewHTTPServer(router, log, address, cfg)
	server.ConnContext = TLSClientCertConnContext(log, DefaultTLSHandshakeTimeout)
	return server
}

// TLSClientCertConnContext returns a ConnContext function that extracts the peer
// TLS certificate from a completed handshake and stores it in the request context.
// Use this when creating an http.Server that requires mTLS client certificate access.
// The handshakeTimeout parameter bounds how long the TLS handshake may take;
// a stalled client is closed so the accept loop can continue serving connections.
func TLSClientCertConnContext(log logrus.FieldLogger, handshakeTimeout time.Duration) func(context.Context, net.Conn) context.Context {
	return func(ctx context.Context, c net.Conn) context.Context {
		tc, ok := c.(*tls.Conn)
		if !ok {
			return ctx
		}
		if handshakeTimeout <= 0 {
			handshakeTimeout = DefaultTLSHandshakeTimeout
		}

		handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
		defer cancel()

		err := tc.HandshakeContext(handshakeCtx)
		if err != nil {
			remoteAddr := tc.RemoteAddr().String()
			log.Errorf("TLS handshake error from %s: %v", remoteAddr, err)
			_ = tc.Close()
			return ctx
		}

		cs := tc.ConnectionState()
		if len(cs.PeerCertificates) == 0 {
			log.Warningf("Warning no TLS Peer Certificates: %v", err)
			return ctx
		}
		peerCertificate := cs.PeerCertificates[0]
		return context.WithValue(ctx, consts.TLSPeerCertificateCtxKey, peerCertificate)
	}
}

// NewTLSListener returns a new TLS listener. If the address is empty, it will
// listen on localhost's next available port.
func NewTLSListener(address string, tlsConfig *tls.Config) (net.Listener, error) {
	if address == "" {
		address = "localhost:0"
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(ln, tlsConfig), nil
}

// ValidateClientTlsCert returns an Unauthenticated error unless the gRPC peer
// presented a client certificate chain that was verified during the TLS
// handshake. On success the supplied context is returned unchanged.
func ValidateClientTlsCert(ctx context.Context) (context.Context, error) {
	if _, err := verifiedClientCert(ctx); err != nil {
		return ctx, err
	}
	return ctx, nil
}

// verifiedClientCert returns the leaf of the first certificate chain verified
// during the gRPC TLS handshake. Unverified peer certificates are never used.
func verifiedClientCert(ctx context.Context) (*x509.Certificate, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "no peer found")
	}

	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return nil, status.Error(codes.Unauthenticated, "failed to verify client certificate")
	}
	return tlsInfo.State.VerifiedChains[0][0], nil
}
