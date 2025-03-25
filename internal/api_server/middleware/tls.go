package middleware

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type contextKey string

const TLSCommonNameContextKey contextKey = "tls-cn"

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

func NewHTTPServerWithTLSContext(router http.Handler, log logrus.FieldLogger, address string, cfg *config.Config) *http.Server {
	server := NewHTTPServer(router, log, address, cfg)
	server.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		tc := c.(*tls.Conn)
		// We need to ensure TLS handshake is complete before
		// we try to get anything useful from the ConnectionState
		// tls delays handshake until the first Read of Write
		err := tc.HandshakeContext(ctx)
		if err != nil {
			remoteAddr := tc.RemoteAddr().String()
			log.Errorf("TLS handshake error from %s: %v", remoteAddr, err)
			log.Errorf("TLS ConnectionState: %#v", tc.ConnectionState())
			return ctx
		}

		cs := tc.ConnectionState()
		if len(cs.PeerCertificates) == 0 {
			log.Warningf("Warning no TLS Peer Certificates: %v", err)
			return ctx
		}
		peerCertificate := cs.PeerCertificates[0]
		return context.WithValue(ctx, TLSCommonNameContextKey, peerCertificate.Subject.CommonName)
	}
	return server
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

func ValidateClientTlsCert(ctx context.Context) (context.Context, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "no peer found")
	}

	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return ctx, status.Error(codes.Unauthenticated, "failed to verify client certificate")
	}
	return ctx, nil
}
