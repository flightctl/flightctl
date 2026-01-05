package middleware

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	apiconfig "github.com/flightctl/flightctl/internal/config/api"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// HTTPServerConfig provides HTTP server timeouts and limits configuration.
type HTTPServerConfig struct {
	HttpReadTimeout       util.Duration
	HttpReadHeaderTimeout util.Duration
	HttpWriteTimeout      util.Duration
	HttpIdleTimeout       util.Duration
	HttpMaxHeaderBytes    int
}

// DefaultHTTPServerConfig returns default HTTP server configuration.
func DefaultHTTPServerConfig() *HTTPServerConfig {
	return &HTTPServerConfig{
		HttpReadTimeout:       util.Duration(5 * time.Minute),
		HttpReadHeaderTimeout: util.Duration(5 * time.Minute),
		HttpWriteTimeout:      util.Duration(5 * time.Minute),
		HttpIdleTimeout:       util.Duration(5 * time.Minute),
		HttpMaxHeaderBytes:    32 * 1024, // 32KB
	}
}

func NewHTTPServer(router http.Handler, log logrus.FieldLogger, address string, cfg *apiconfig.Config) *http.Server {
	return NewHTTPServerWithConfig(router, log, address, &HTTPServerConfig{
		HttpReadTimeout:       cfg.Service.HttpReadTimeout,
		HttpReadHeaderTimeout: cfg.Service.HttpReadHeaderTimeout,
		HttpWriteTimeout:      cfg.Service.HttpWriteTimeout,
		HttpIdleTimeout:       cfg.Service.HttpIdleTimeout,
		HttpMaxHeaderBytes:    cfg.Service.HttpMaxHeaderBytes,
	})
}

func NewHTTPServerWithConfig(router http.Handler, log logrus.FieldLogger, address string, cfg *HTTPServerConfig) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           router,
		ReadTimeout:       time.Duration(cfg.HttpReadTimeout),
		ReadHeaderTimeout: time.Duration(cfg.HttpReadHeaderTimeout),
		WriteTimeout:      time.Duration(cfg.HttpWriteTimeout),
		IdleTimeout:       time.Duration(cfg.HttpIdleTimeout),
		MaxHeaderBytes:    cfg.HttpMaxHeaderBytes,
	}
}

func NewHTTPServerWithTLSContext(router http.Handler, log logrus.FieldLogger, address string, cfg *apiconfig.Config) *http.Server {
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
		return context.WithValue(ctx, consts.TLSPeerCertificateCtxKey, peerCertificate)
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
