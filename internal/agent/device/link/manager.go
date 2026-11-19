// Package link keeps the agent's persistent connection to the agent-facing
// API endpoint open.
//
// The agent is the WebSocket client because it originates the connection
// outbound over mTLS; nothing here gives the device an inbound listener.
// After the upgrade the connection is symmetric and either peer may send at
// any time.
//
// The manager owns dialling, redialling and shutdown. Everything that happens
// over an established connection -- the reader, the writer, the supervisor
// and the attribution rules -- is the shared link machinery, so both ends of
// the link enforce one contract rather than two similar ones.
package link

import (
	"context"
	"crypto/tls"
	"errors"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/agent/identity"
	"github.com/flightctl/flightctl/internal/agentlink/link"
	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/gorilla/websocket"
)

// Manager maintains exactly one link to the agent-facing endpoint.
type Manager struct {
	identity identity.Provider
	config   *baseclient.Config
	log      *log.PrefixLogger

	// handlers is the closed, compiled-in allowlist of methods the agent
	// implements in the responder role. A method absent from it is answered
	// with exactly one MethodNotSupported result and starts no work.
	//
	// It is fixed for the life of the manager and shared unchanged by every
	// link the manager opens: reconnect is recovery, not renegotiation, so
	// what the agent implements never depends on which connection a request
	// arrived on.
	handlers map[string]link.Handler

	// The three seams below are indirected so tests can drive the manager
	// without certificate material, a real endpoint or real backoff delays.
	tlsConfig  func() (*tls.Config, error)
	dial       func(ctx context.Context, url string, tlsConfig *tls.Config) (*websocket.Conn, *http.Response, error)
	backoffFor func(attempt int) time.Duration
}

// NewManager returns a link manager for the given management service
// configuration, serving the given methods in the responder role.
func NewManager(identityProvider identity.Provider, config *baseclient.Config, logger *log.PrefixLogger, handlers map[string]link.Handler) *Manager {
	m := &Manager{
		identity:   identityProvider,
		config:     config,
		log:        logger,
		handlers:   handlers,
		dial:       dialLink,
		backoffFor: backoff,
	}
	m.tlsConfig = func() (*tls.Config, error) {
		return m.identity.CreateTLSConfig(m.config)
	}
	return m
}

// Run keeps one link open until ctx is cancelled.
//
// Reconnect is recovery, never resumption. The link is expected to break
// periodically, so redialling with jittered backoff is normal operation. The
// replacement carries nothing over: every request in flight on the broken
// link was failed explicitly and individually, and the new link starts with
// no requests. A caller that wants its data again issues a new request.
func (m *Manager) Run(ctx context.Context) {
	m.log.Info("Starting agent link manager")
	defer m.log.Info("Agent link manager stopped")

	attempt := 0
	for {
		// Shutdown step 1: stop redialling, so a link that breaks from here
		// is not replaced. Cancelling ctx is what reaches this check.
		if ctx.Err() != nil {
			return
		}

		connected, err := m.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}

		if connected {
			// The link came up and later ended. Treat the next dial as a
			// fresh sequence rather than continuing to grow the delay from
			// a long-finished failure.
			m.log.Infof("Agent link ended, redialling: %v", err)
			attempt = 0
		} else {
			m.log.Debugf("Agent link dial failed: %v", err)
		}

		delay := m.backoffFor(attempt)
		attempt++

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// runOnce dials, serves one link to its end, and reports whether the
// connection was ever established.
func (m *Manager) runOnce(ctx context.Context) (bool, error) {
	baseTLS, err := m.tlsConfig()
	if err != nil {
		return false, err
	}
	url, err := linkURL(m.config)
	if err != nil {
		return false, err
	}

	conn, resp, err := m.dial(ctx, url, linkTLSConfig(baseTLS))
	if err != nil {
		return false, describeDialFailure(err, resp)
	}

	// The agent must verify the token it was actually given. Dialer.Dial
	// validates the status line, Upgrade, Connection and Sec-WebSocket-Accept
	// and nothing else, then assigns whatever the response carried --
	// including an empty string -- into the connection's subprotocol field.
	// Neither side relies on the other for this.
	if got := conn.Subprotocol(); got != protocol.Subprotocol {
		_ = conn.Close()
		// A failed dial: no application message is sent and nothing is
		// registered.
		return false, &dialError{reason: "subprotocol mismatch", detail: got}
	}

	m.log.Infof("Agent link established to %s", url)
	err = link.Serve(ctx, conn, link.Config{
		Role:     protocol.OriginAgent,
		Handlers: m.handlers,
		Log:      m.log,
	})
	return true, err
}

func dialLink(ctx context.Context, url string, tlsConfig *tls.Config) (*websocket.Conn, *http.Response, error) {
	// The dial context governs connection establishment only. After success
	// the library clears the deadline it set, so the link's lifetime belongs
	// to the supervisor loop and not to this context.
	return newDialer(tlsConfig).DialContext(ctx, url, nil)
}

// backoff returns the delay before attempt n: exponential with full jitter,
// so the first retry is drawn uniformly from 0-30s.
//
// Full jitter rather than a fixed schedule because a fleet whose agents all
// lost a link to the same restart must not redial in step.
func backoff(attempt int) time.Duration {
	window := protocol.ReconnectBackoffBase
	for i := 0; i < attempt && window < protocol.ReconnectBackoffCap; i++ {
		window *= 2
	}
	if window > protocol.ReconnectBackoffCap {
		window = protocol.ReconnectBackoffCap
	}
	// The jitter spreads a fleet's redials; it is not a secret and nothing
	// authenticates on it, so the cheap generator is the right one.
	return time.Duration(rand.Int64N(int64(window))) //nolint:gosec // schedule jitter, not a security decision
}

// dialError describes a dial that failed after TLS succeeded.
type dialError struct {
	reason string
	detail string
}

func (e *dialError) Error() string {
	if e.detail == "" {
		return e.reason
	}
	return e.reason + ": " + e.detail
}

// describeDialFailure keeps the three failure domains distinct.
//
// A TLS failure produces no HTTP status at all -- nothing was requested, so
// nothing was refused. A pre-101 refusal produces an ordinary HTTP response,
// which the library returns alongside ErrBadHandshake. After the 101 there is
// no HTTP status ever again, which is why this function is only reachable
// before one.
func describeDialFailure(err error, resp *http.Response) error {
	if !errors.Is(err, websocket.ErrBadHandshake) || resp == nil {
		// No HTTP response: the TLS handshake or the connection itself
		// failed.
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return &dialError{reason: "agent link route is not available on this service"}
	case http.StatusUpgradeRequired:
		return &dialError{reason: "service requires HTTP/1.1 for the agent link"}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &dialError{reason: "service refused the agent link", detail: resp.Status}
	default:
		return &dialError{reason: "agent link handshake failed", detail: resp.Status}
	}
}
