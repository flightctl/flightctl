package agentserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agentlink"
	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	fcmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

const (
	testDevice = "device-fingerprint"
	testOrg    = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
)

// fakeKV is an in-memory stand-in for the parts of the KV store the
// registry uses, including the compare-and-set semantics the dispatch key
// depends on.
type fakeKV struct {
	kvstore.KVStore

	mu     sync.Mutex
	values map[string][]byte
	closed bool
}

func newFakeKV() *fakeKV { return &fakeKV{values: map[string][]byte{}} }

func (f *fakeKV) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeKV) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeKV) SetNX(_ context.Context, key string, value []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.values[key]; exists {
		return false, nil
	}
	f.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (f *fakeKV) SetExpire(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (f *fakeKV) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = append([]byte(nil), value...)
	return nil
}

func (f *fakeKV) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[key], nil
}

func (f *fakeKV) ExpireIfMatch(_ context.Context, key string, expected []byte, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.values[key]) == string(expected), nil
}

func (f *fakeKV) DeleteIfMatch(_ context.Context, key string, expected []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if string(f.values[key]) != string(expected) {
		return false, nil
	}
	delete(f.values, key)
	return true, nil
}

func (f *fakeKV) get(key string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[key]
}

// withAuthenticatedAgent injects what the agent middleware chain would have
// established before the route runs.
func withAuthenticatedAgent(next http.Handler, device, org string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := fcmiddleware.NewAgentIdentity(device, org, "CN="+device, time.Now().Add(time.Hour))
		ctx := context.WithValue(r.Context(), consts.IdentityCtxKey, identity)
		ctx = util.WithOrganizationID(ctx, uuid.MustParse(org))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newLinkTestServer(t *testing.T, kv kvstore.KVStore, authenticated bool) *httptest.Server {
	t.Helper()

	log := logrus.New()
	log.SetOutput(io.Discard)

	handler := newAgentLinkHandler(agentlink.NewRegistry(kv, "replica-under-test", log), context.Background(), log)

	var root http.Handler = handler
	if authenticated {
		root = withAuthenticatedAgent(handler, testDevice, testOrg)
	}

	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv
}

func TestAgentLinkRefusalsHappenBeforeTheUpgrade(t *testing.T) {
	testCases := []struct {
		name       string
		headers    map[string]string
		wantStatus int
	}{
		{
			name: "When the request carries an Origin header it should be refused with 403",
			headers: map[string]string{
				"Connection":             "Upgrade",
				"Upgrade":                "websocket",
				"Sec-WebSocket-Version":  "13",
				"Sec-WebSocket-Key":      "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Protocol": protocol.Subprotocol,
				"Origin":                 "https://example.com",
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "When the subprotocol is not offered it should be refused with 400",
			headers: map[string]string{
				"Connection":            "Upgrade",
				"Upgrade":               "websocket",
				"Sec-WebSocket-Version": "13",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "When a different subprotocol is offered it should be refused with 400",
			headers: map[string]string{
				"Connection":             "Upgrade",
				"Upgrade":                "websocket",
				"Sec-WebSocket-Version":  "13",
				"Sec-WebSocket-Key":      "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Protocol": "flightctl.agentlink.v99",
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			srv := newLinkTestServer(t, newFakeKV(), true)

			req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
			require.NoError(err)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			resp, err := srv.Client().Do(req)
			require.NoError(err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(tc.wantStatus, resp.StatusCode)
		})
	}
}

// TestAgentLinkRefusesNonHTTP11 covers the case the protocol multiplexer
// makes reachable: a non-gRPC HTTP/2 request reaches this same handler, and
// Go's HTTP/2 ResponseWriter is not a Hijacker, so the route answers 426
// itself rather than letting the library produce an opaque 500.
func TestAgentLinkRefusesNonHTTP11(t *testing.T) {
	require := require.New(t)

	log := logrus.New()
	log.SetOutput(io.Discard)
	handler := newAgentLinkHandler(agentlink.NewRegistry(newFakeKV(), "replica", log), context.Background(), log)

	req := httptest.NewRequest(http.MethodGet, AgentLinkPath, nil)
	req.Proto = "HTTP/2.0"
	req.ProtoMajor = 2
	req.ProtoMinor = 0
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Protocol", protocol.Subprotocol)

	rec := httptest.NewRecorder()
	withAuthenticatedAgent(handler, testDevice, testOrg).ServeHTTP(rec, req)

	require.Equal(http.StatusUpgradeRequired, rec.Code)
	require.Equal("HTTP/1.1", rec.Header().Get("Upgrade"))
}

func TestAgentLinkRefusesWithoutAnAgentIdentity(t *testing.T) {
	require := require.New(t)

	// No middleware established an identity: the route must not upgrade.
	srv := newLinkTestServer(t, newFakeKV(), false)

	dialer := &websocket.Dialer{Subprotocols: []string{protocol.Subprotocol}}
	conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)

	require.Error(err)
	if conn != nil {
		_ = conn.Close()
	}
	require.NotNil(resp)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(http.StatusUnauthorized, resp.StatusCode)
}

func TestAgentLinkUpgradesAndRegistersTheLink(t *testing.T) {
	require := require.New(t)

	kv := newFakeKV()
	srv := newLinkTestServer(t, kv, true)

	dialer := &websocket.Dialer{Subprotocols: []string{protocol.Subprotocol}}
	conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(err)
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()

	require.Equal(http.StatusSwitchingProtocols, resp.StatusCode)
	require.Equal(protocol.Subprotocol, conn.Subprotocol())

	// The registration appears under the dispatch key for the verified
	// device and organization, which come only from the identity.
	key := (&kvstore.AgentLinkDispatchKey{
		OrgID:      uuid.MustParse(testOrg),
		DeviceName: testDevice,
	}).ComposeKey()

	require.Eventually(func() bool { return kv.get(key) != nil }, 5*time.Second, 10*time.Millisecond)
	require.Contains(string(kv.get(key)), "replica-under-test")
	require.Contains(string(kv.get(key)), "linkId")
}

// TestAgentLinkAnswersUnimplementedMethod confirms the mounted route runs the
// real endpoint: no method is implemented, so a well-formed agent-originated
// request is refused with exactly one MethodNotSupported.
func TestAgentLinkAnswersUnimplementedMethod(t *testing.T) {
	require := require.New(t)

	srv := newLinkTestServer(t, newFakeKV(), true)

	dialer := &websocket.Dialer{Subprotocols: []string{protocol.Subprotocol}}
	conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(err)
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()

	request := `{"v":1,"kind":"request","origin":"agent","id":"a1","method":"service.something","params":{}}`
	require.NoError(conn.WriteMessage(websocket.TextMessage, []byte(request)))

	require.NoError(conn.SetReadDeadline(time.Now().Add(5 * time.Second)))
	_, data, err := conn.ReadMessage()
	require.NoError(err)

	env, err := protocol.Decode(data)
	require.NoError(err)
	require.Equal(protocol.KindResult, env.Kind)
	require.Equal("a1", env.ID)
	require.Equal(protocol.ReasonMethodNotSupported, env.Status.Reason)
}

func TestAgentLinkReleasesItsRegistrationWhenTheLinkEnds(t *testing.T) {
	require := require.New(t)

	kv := newFakeKV()
	srv := newLinkTestServer(t, kv, true)

	dialer := &websocket.Dialer{Subprotocols: []string{protocol.Subprotocol}}
	conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	key := (&kvstore.AgentLinkDispatchKey{
		OrgID:      uuid.MustParse(testOrg),
		DeviceName: testDevice,
	}).ComposeKey()
	require.Eventually(func() bool { return kv.get(key) != nil }, 5*time.Second, 10*time.Millisecond)

	require.NoError(conn.Close())

	require.Eventually(func() bool { return kv.get(key) == nil }, 5*time.Second, 10*time.Millisecond)
}

func TestServerShutdownWaitsForLinksBeforeKVClose(t *testing.T) {
	// Regression: AgentServer.Run called Stop (kvStore.Close) immediately
	// after srv.Shutdown, but hijacked WebSocket connections are not awaited
	// by srv.Shutdown. The fix adds WaitForLinks between cancellation and
	// Stop, and the handler tracks active links with a WaitGroup.
	//
	// This test proves the ordering: an active link must release its
	// registration before KV is considered closeable.
	require := require.New(t)

	kv := newFakeKV()
	log := logrus.New()
	log.SetOutput(io.Discard)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	handler := newAgentLinkHandler(agentlink.NewRegistry(kv, "replica-shutdown-test", log), serverCtx, log)

	var root http.Handler = withAuthenticatedAgent(handler, testDevice, testOrg)
	srv := httptest.NewServer(root)
	defer srv.Close()

	// Establish a link.
	dialer := &websocket.Dialer{Subprotocols: []string{protocol.Subprotocol}}
	conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	defer func() { _ = conn.Close() }()

	key := (&kvstore.AgentLinkDispatchKey{
		OrgID:      uuid.MustParse(testOrg),
		DeviceName: testDevice,
	}).ComposeKey()
	require.Eventually(func() bool { return kv.get(key) != nil }, 5*time.Second, 10*time.Millisecond,
		"link must register before we test shutdown")

	// Cancel the server context to begin ordered shutdown.
	serverCancel()

	// WaitForLinks must block until the link's runLink (and its deferred
	// lease Release) have returned.
	waitDone := make(chan struct{})
	go func() {
		handler.WaitForLinks()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("WaitForLinks did not return; active links are not draining")
	}

	// After WaitForLinks returns, the registration should have been released.
	require.Nil(kv.get(key), "lease must be released before WaitForLinks returns")

	// KV must NOT have been closed before WaitForLinks returned.
	// (In production, Stop() calls kv.Close() after WaitForLinks.)
	// We can now safely close it.
	require.False(kv.isClosed(), "KV must not be closed until after links drain")
	kv.Close()
	require.True(kv.isClosed())
}

func TestShutdownRefusesNewLinksWhileDraining(t *testing.T) {
	// After WaitForLinks sets draining, new upgrade attempts must be refused
	// to prevent an Add-after-Wait race on the WaitGroup.
	require := require.New(t)

	kv := newFakeKV()
	log := logrus.New()
	log.SetOutput(io.Discard)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	handler := newAgentLinkHandler(agentlink.NewRegistry(kv, "replica-drain-test", log), serverCtx, log)

	// Set draining manually.
	handler.mu.Lock()
	handler.draining = true
	handler.mu.Unlock()

	var root http.Handler = withAuthenticatedAgent(handler, testDevice, testOrg)
	srv := httptest.NewServer(root)
	defer srv.Close()

	// Attempt to connect. The upgrade must fail or the connection must be
	// closed immediately.
	dialer := &websocket.Dialer{Subprotocols: []string{protocol.Subprotocol}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err == nil {
		// Connection established but should be immediately closed.
		_, _, readErr := conn.ReadMessage()
		require.Error(readErr, "connection must be closed during draining")
		_ = conn.Close()
	}
	// Either dial failed or read failed — both are correct.

	key := (&kvstore.AgentLinkDispatchKey{
		OrgID:      uuid.MustParse(testOrg),
		DeviceName: testDevice,
	}).ComposeKey()
	require.Nil(kv.get(key), "no registration must be created during draining")
}
