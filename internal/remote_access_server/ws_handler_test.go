package remote_access_server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestTruncateWSCloseReason(t *testing.T) {
	t.Parallel()

	short := "app not found"
	if got := truncateWSCloseReason(short); got != short {
		t.Errorf("expected short reason to pass through unchanged, got %q", got)
	}

	long := strings.Repeat("x", maxWSCloseReasonBytes+50)
	got := truncateWSCloseReason(long)
	if len(got) != maxWSCloseReasonBytes {
		t.Errorf("expected truncated reason to be %d bytes, got %d", maxWSCloseReasonBytes, len(got))
	}

	// A multi-byte rune ("é" is 2 bytes in UTF-8) repeated so that a naive byte-slice at
	// maxWSCloseReasonBytes (odd) falls in the middle of the final rune.
	multibyte := strings.Repeat("é", maxWSCloseReasonBytes)
	gotMultibyte := truncateWSCloseReason(multibyte)
	if len(gotMultibyte) > maxWSCloseReasonBytes {
		t.Errorf("expected truncated multi-byte reason to be at most %d bytes, got %d", maxWSCloseReasonBytes, len(gotMultibyte))
	}
	if !utf8.ValidString(gotMultibyte) {
		t.Errorf("expected truncated multi-byte reason to be valid UTF-8, got %q", gotMultibyte)
	}
}

// fakeAppDeviceService is a minimal AppConsoleDeviceService stub that always succeeds,
// so tests can drive AppConsoleSessionManager without a real store.
type fakeAppDeviceService struct{}

func (f *fakeAppDeviceService) GetDevice(_ context.Context, _ uuid.UUID, name string) (*domain.Device, domain.Status) {
	return &domain.Device{
		Metadata: domain.ObjectMeta{Name: &name, Annotations: &map[string]string{}},
		Spec:     &domain.DeviceSpec{},
	}, domain.StatusOK()
}

func (f *fakeAppDeviceService) UpdateDevice(_ context.Context, _ uuid.UUID, _ string, device domain.Device, _ []string) (*domain.Device, error) {
	return &device, nil
}

// fakeConsoleEventNotifier is a no-op console.ConsoleEventNotifier stub.
type fakeConsoleEventNotifier struct{}

func (f *fakeConsoleEventNotifier) NotifyConsole(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}

func (f *fakeConsoleEventNotifier) ClearConsoleNotification(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}

// fakeAppSessionRegistration reports every started session on startedCh, mimicking the
// gRPC server that would otherwise rendezvous with the agent's Stream() call.
type fakeAppSessionRegistration struct {
	startedCh chan *console.AppConsoleSession
}

func (f *fakeAppSessionRegistration) StartSession(session *console.AppConsoleSession) error {
	f.startedCh <- session
	return nil
}

func (f *fakeAppSessionRegistration) CloseSession(_ *console.AppConsoleSession) error {
	return nil
}

func TestHandleApplicationConsole_AgentError_ClosesWithCustomCode(t *testing.T) {
	t.Parallel()

	startedCh := make(chan *console.AppConsoleSession, 1)
	mgr := console.NewAppConsoleSessionManager(
		&fakeAppDeviceService{},
		logrus.NewEntry(logrus.New()),
		&fakeAppSessionRegistration{startedCh: startedCh},
		&fakeConsoleEventNotifier{},
	)
	handler := NewAppConsoleHandler(logrus.New(), mgr)

	router := chi.NewRouter()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/v1/devices/device1/applications/app1/console?consoleType=serial"

	type dialResult struct {
		conn *websocket.Conn
		err  error
	}
	dialDone := make(chan dialResult, 1)
	dialer := websocket.Dialer{Subprotocols: []string{"serial"}}
	go func() {
		conn, _, err := dialer.Dial(wsURL, nil)
		dialDone <- dialResult{conn: conn, err: err}
	}()

	var session *console.AppConsoleSession
	select {
	case session = <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected StartSession to be called")
	}

	// The server-side handler is blocked selecting on session.ProtocolCh; simulate the
	// agent connecting and negotiating a protocol so the WebSocket handshake completes.
	select {
	case session.ProtocolCh <- "serial":
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending selected protocol")
	}

	var res dialResult
	select {
	case res = <-dialDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket handshake")
	}
	require.NoError(t, res.err)
	conn := res.conn
	defer conn.Close()

	// Simulate the agent reporting a session-level failure (e.g. app not found).
	const agentErr = "app is not a VM workload"
	select {
	case session.ErrCh <- agentErr:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending agent error")
	}

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err, "expected the connection to be closed by the server")

	closeErr, ok := err.(*websocket.CloseError)
	require.True(t, ok, "expected a *websocket.CloseError, got %T: %v", err, err)
	require.Equal(t, consts.AppConsoleErrorCloseCode, closeErr.Code)
	require.Equal(t, agentErr, closeErr.Text)
}

// TestHandleApplicationConsole_AgentError_BeforeProtocolSelection_ReturnsNotFound covers the
// other agent-error path: one reported before a protocol was ever selected, which must fail
// the handshake itself (plain HTTP 404) rather than upgrade and close with a custom code, so
// the client never observes a false "connected" state.
func TestHandleApplicationConsole_AgentError_BeforeProtocolSelection_ReturnsNotFound(t *testing.T) {
	t.Parallel()

	startedCh := make(chan *console.AppConsoleSession, 1)
	mgr := console.NewAppConsoleSessionManager(
		&fakeAppDeviceService{},
		logrus.NewEntry(logrus.New()),
		&fakeAppSessionRegistration{startedCh: startedCh},
		&fakeConsoleEventNotifier{},
	)
	handler := NewAppConsoleHandler(logrus.New(), mgr)

	router := chi.NewRouter()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/v1/devices/device1/applications/app1/console?consoleType=serial"

	type dialResult struct {
		conn *websocket.Conn
		resp *http.Response
		err  error
	}
	dialDone := make(chan dialResult, 1)
	dialer := websocket.Dialer{Subprotocols: []string{"serial"}}
	go func() {
		conn, resp, err := dialer.Dial(wsURL, nil)
		dialDone <- dialResult{conn: conn, resp: resp, err: err}
	}()

	var session *console.AppConsoleSession
	select {
	case session = <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected StartSession to be called")
	}

	// The agent fails to resolve the app before ever negotiating a protocol, e.g. the
	// requested app does not exist.
	const agentErr = "app is not a VM workload"
	select {
	case session.ErrCh <- agentErr:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending agent error")
	}

	var res dialResult
	select {
	case res = <-dialDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the dial to fail")
	}
	require.Error(t, res.err, "expected the WebSocket handshake to fail rather than upgrade")
	require.Nil(t, res.conn)
	require.NotNil(t, res.resp)
	require.Equal(t, http.StatusNotFound, res.resp.StatusCode)

	body, readErr := io.ReadAll(res.resp.Body)
	require.NoError(t, readErr)
	require.Equal(t, agentErr+"\n", string(body))
}
