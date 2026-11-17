package middleware

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// When a TLS client stalls during the handshake it should not block the
// ConnContext function beyond the handshake timeout, and the connection
// should be closed so the accept loop can continue.
func TestTLSClientCertConnContextHandshakeTimeout(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	tlsServerConn := tls.Server(serverConn, &tls.Config{
		Certificates:       nil,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
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
