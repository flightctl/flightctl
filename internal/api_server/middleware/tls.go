package middleware

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type contextKey string

const TLSCommonNameContextKey contextKey = "tls-cn"

func NewHTTPServer(router http.Handler, log logrus.FieldLogger, address string) *http.Server {
	return &http.Server{
		Addr:         address,
		Handler:      router,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
}

func NewHTTPServerWithTLSContext(router http.Handler, log logrus.FieldLogger, address string) *http.Server {
	server := NewHTTPServer(router, log, address)
	server.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		tc := c.(*tls.Conn)
		// We need to ensure TLS handshake is complete before
		// we try to get anything useful from the ConnectionState
		// tls delays handshake until the first Read of Write
		err := tc.Handshake()
		if err != nil {
			log.Errorf("TLS handshake error: %v", err)
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
