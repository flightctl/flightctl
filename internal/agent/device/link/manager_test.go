package link

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agentlink/link"
	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

const testTimeout = 10 * time.Second

// serviceSide runs the real service-side endpoint, so these tests exercise
// both halves of the link against each other rather than against a stub.
type serviceSide struct {
	url string
	// endpoints receives each endpoint as its connection is established.
	endpoints chan *link.Endpoint
	// dials counts how many times the agent completed the handshake.
	dials atomic.Int64
	// sever closes the most recent connection, to exercise reconnect.
	sever chan struct{}
}

func newServiceSide(t *testing.T) *serviceSide {
	t.Helper()

	logger := log.NewPrefixLogger("test")
	logger.SetOutput(discard{})

	s := &serviceSide{
		endpoints: make(chan *link.Endpoint, 8),
		sever:     make(chan struct{}, 8),
	}

	upgrader := websocket.Upgrader{Subprotocols: []string{protocol.Subprotocol}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.dials.Add(1)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			select {
			case <-s.sever:
				_ = conn.Close()
			case <-ctx.Done():
			}
		}()

		_ = link.Serve(ctx, conn, link.Config{
			Role: protocol.OriginService,
			Log:  logger,
			OnConnect: func(e *link.Endpoint) {
				select {
				case s.endpoints <- e:
				default:
				}
			},
		})
	}))
	t.Cleanup(srv.Close)

	s.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return s
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// newTestManager builds a manager that dials the test server directly. The
// TLS config is bypassed because these tests are about the link's behaviour,
// not about certificate plumbing, which dialer_test.go covers separately.
func newTestManager(t *testing.T, url string) *Manager {
	t.Helper()

	logger := log.NewPrefixLogger("test-agent")
	logger.SetOutput(discard{})

	cfg := &baseclient.Config{}
	cfg.Service.Server = "https://example.invalid:7443"

	m := &Manager{
		config: cfg,
		log:    logger,
		dial: func(ctx context.Context, _ string, _ *tls.Config) (*websocket.Conn, *http.Response, error) {
			d := &websocket.Dialer{Subprotocols: []string{protocol.Subprotocol}}
			return d.DialContext(ctx, url, nil)
		},
	}
	// The identity provider is not consulted because tlsConfigFn is
	// overridden below.
	m.tlsConfig = func() (*tls.Config, error) { return &tls.Config{MinVersion: tls.VersionTLS13}, nil }
	return m
}

func (s *serviceSide) nextEndpoint(t *testing.T) *link.Endpoint {
	t.Helper()
	select {
	case e := <-s.endpoints:
		return e
	case <-time.After(testTimeout):
		t.Fatal("the agent did not establish a link")
		return nil
	}
}

// TestAgentAnswersUnimplementedMethod is the end-to-end shape of the current
// build: the link comes up, carries the envelope, and refuses every method
// because none is implemented yet.
func TestAgentAnswersUnimplementedMethod(t *testing.T) {
	require := require.New(t)

	service := newServiceSide(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := newTestManager(t, service.url)
	go m.Run(ctx)

	endpoint := service.nextEndpoint(t)

	originated, err := endpoint.Originate("req-1", "device.logs.get", nil, nil)
	require.NoError(err)

	select {
	case ev := <-originated.Events():
		require.Equal(protocol.KindResult, ev.Kind)
		require.Equal(protocol.ReasonMethodNotSupported, ev.Status.Reason)
	case <-time.After(testTimeout):
		t.Fatal("no result from the agent")
	}
}

// TestManagerRedialsAfterTheLinkIsSevered covers reconnect as recovery: the
// replacement link starts clean rather than resuming anything.
func TestManagerRedialsAfterTheLinkIsSevered(t *testing.T) {
	require := require.New(t)

	service := newServiceSide(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := newTestManager(t, service.url)
	// Keep the test quick; the jitter distribution itself is covered in
	// dialer_test.go.
	m.backoffFor = func(int) time.Duration { return 10 * time.Millisecond }
	go m.Run(ctx)

	first := service.nextEndpoint(t)
	require.NotNil(first)

	// Sever the connection underneath the agent.
	service.sever <- struct{}{}

	second := service.nextEndpoint(t)
	require.NotNil(second)
	require.NotSame(first, second, "the replacement must be a new endpoint, not the old one resumed")

	// The replacement link is fully usable and starts with no requests.
	originated, err := second.Originate("req-after-reconnect", "device.logs.get", nil, nil)
	require.NoError(err)

	select {
	case ev := <-originated.Events():
		require.Equal(protocol.ReasonMethodNotSupported, ev.Status.Reason)
	case <-time.After(testTimeout):
		t.Fatal("the replacement link did not answer")
	}

	require.GreaterOrEqual(service.dials.Load(), int64(2))
}

// TestManagerStopsRediallingWhenCancelled is shutdown step 1: a link that
// breaks after cancellation is not replaced.
func TestManagerStopsRediallingWhenCancelled(t *testing.T) {
	require := require.New(t)

	service := newServiceSide(t)
	ctx, cancel := context.WithCancel(context.Background())

	m := newTestManager(t, service.url)
	m.backoffFor = func(int) time.Duration { return 10 * time.Millisecond }

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx)
	}()

	service.nextEndpoint(t)
	cancel()

	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("the manager did not stop when its context was cancelled")
	}

	before := service.dials.Load()
	time.Sleep(200 * time.Millisecond)
	require.Equal(before, service.dials.Load(), "the manager must not redial after it has stopped")
}

// TestManagerRejectsAMismatchedSubprotocol covers the agent's own check: the
// library validates the status line and accept key but assigns whatever
// subprotocol the response carried, including none.
func TestManagerRejectsAMismatchedSubprotocol(t *testing.T) {
	require := require.New(t)

	// A server that upgrades without echoing the subprotocol.
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Hold the connection open so a failure to hang up is observable.
		_, _, _ = conn.ReadMessage()
	}))
	defer srv.Close()

	m := newTestManager(t, "ws"+strings.TrimPrefix(srv.URL, "http"))

	connected, err := m.runOnce(context.Background())

	require.False(connected, "a subprotocol mismatch is a failed dial, not an established link")
	require.Error(err)
	require.Contains(err.Error(), "subprotocol mismatch")
}
