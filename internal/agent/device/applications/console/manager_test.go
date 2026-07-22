package console

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// mockExecStreamer is a minimal ExecStreamer for tests.
type mockExecStreamer struct {
	conn io.ReadWriteCloser
	err  error
}

func (m *mockExecStreamer) ExecStream(_ context.Context, _ string, _ ...string) (io.ReadWriteCloser, error) {
	return m.conn, m.err
}

// mockResolver is a minimal AppConsoleResolver for tests. When seq[appName] is non-empty,
// each call pops and returns the next entry (letting a test hand out a different Session per
// resolution, e.g. for a --force takeover scenario); once exhausted it falls back to sessions.
type mockResolver struct {
	mu       sync.Mutex
	sessions map[string]Session
	seq      map[string][]Session
	err      map[string]error
}

func (m *mockResolver) ResolveConsole(appName, _ string) (Session, error) {
	if err, ok := m.err[appName]; ok {
		return nil, err
	}
	m.mu.Lock()
	if q, ok := m.seq[appName]; ok && len(q) > 0 {
		m.seq[appName] = q[1:]
		m.mu.Unlock()
		return q[0], nil
	}
	m.mu.Unlock()
	if s, ok := m.sessions[appName]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("app %q not found", appName)
}

// noopSession is a Session that returns immediately.
type noopSession struct{}

func (noopSession) Run(_ context.Context, streamClient grpc_v1.RouterService_StreamClient) {
	_ = streamClient.CloseSend()
}

type testVars struct {
	ctx              context.Context
	manager          *Manager
	ctrl             *gomock.Controller
	mockGrpcClient   *MockRouterServiceClient
	mockStreamClient *MockRouterService_StreamClient
	resolver         *mockResolver
	logger           *log.PrefixLogger
	sentRequests     []*grpc_v1.StreamRequest
	mu               sync.Mutex
	once             sync.Once
	recvChan         chan lo.Tuple2[*grpc_v1.StreamResponse, error]
	closeSendCalled  bool
	streamCtx        context.Context
}

func setupTestVars(t *testing.T, resolver *mockResolver) *testVars {
	t.Helper()
	ctrl := gomock.NewController(t)
	logger := log.NewPrefixLogger("test")
	mockGrpcClient := NewMockRouterServiceClient(ctrl)
	mockStreamClient := NewMockRouterService_StreamClient(ctrl)
	if resolver == nil {
		resolver = &mockResolver{}
	}

	v := &testVars{
		ctx:              context.Background(),
		ctrl:             ctrl,
		mockGrpcClient:   mockGrpcClient,
		mockStreamClient: mockStreamClient,
		resolver:         resolver,
		logger:           logger,
		recvChan:         make(chan lo.Tuple2[*grpc_v1.StreamResponse, error]),
	}

	v.manager = NewManager(
		mockGrpcClient,
		"test-device",
		resolver,
		logger,
	)

	t.Cleanup(func() { ctrl.Finish() })
	return v
}

func (v *testVars) mockStream() {
	v.mockGrpcClient.EXPECT().Stream(gomock.Any()).DoAndReturn(func(ctx context.Context, _ ...grpc.CallOption) (grpc_v1.RouterService_StreamClient, error) {
		v.mu.Lock()
		v.streamCtx = ctx
		v.mu.Unlock()
		return v.mockStreamClient, nil
	})
}

func (v *testVars) mockStreamError(err error) {
	v.mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(nil, err)
}

// sentMetadataValue returns the single value for key in the outgoing metadata of the
// context passed to Stream(), or "" if absent. Panics if mockStream() was not set up
// or Stream() has not yet been called.
func (v *testVars) sentMetadataValue(key string) string {
	v.mu.Lock()
	ctx := v.streamCtx
	v.mu.Unlock()
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(key)
	if len(vals) != 1 {
		return ""
	}
	return vals[0]
}

func (v *testVars) mockSend() {
	v.mockStreamClient.EXPECT().Send(gomock.Any()).DoAndReturn(func(req *grpc_v1.StreamRequest) error {
		v.mu.Lock()
		v.sentRequests = append(v.sentRequests, req)
		v.mu.Unlock()
		return nil
	}).AnyTimes()
}

// lastSentError returns the Error field of the most recently sent StreamRequest, or ""
// if no request has been sent yet.
func (v *testVars) lastSentError() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.sentRequests) == 0 {
		return ""
	}
	return v.sentRequests[len(v.sentRequests)-1].GetError()
}

func (v *testVars) mockRecv() {
	v.mockStreamClient.EXPECT().Recv().DoAndReturn(func() (*grpc_v1.StreamResponse, error) {
		val, ok := <-v.recvChan
		if !ok {
			return nil, io.EOF
		}
		return val.A, val.B
	}).AnyTimes()
}

func (v *testVars) mockCloseSend() {
	v.mockStreamClient.EXPECT().CloseSend().DoAndReturn(func() error {
		v.once.Do(func() {
			v.mu.Lock()
			v.closeSendCalled = true
			v.mu.Unlock()
			close(v.recvChan)
		})
		return nil
	}).AnyTimes()
}

func (v *testVars) sendEOF() {
	v.once.Do(func() {
		close(v.recvChan)
	})
}

// waitSessionsTimeout bounds how long tests wait for a manager's session goroutines to
// finish. Without a deadline, a regression in session teardown would hang the test (and CI)
// indefinitely instead of failing with a clear message.
const waitSessionsTimeout = 5 * time.Second

// waitForSessions blocks until m's session goroutines have all finished, or fails t after
// waitSessionsTimeout.
func waitForSessions(t *testing.T, m *Manager) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		m.sessionWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitSessionsTimeout):
		t.Fatal("timed out waiting for console session goroutines to finish")
	}
}

func (v *testVars) waitSessions(t *testing.T) {
	t.Helper()
	waitForSessions(t, v.manager)
}

func makeDevice(sessions []v1beta1.DeviceRemoteSession) *v1beta1.Device {
	annotations := make(map[string]string)
	if len(sessions) > 0 {
		b, _ := json.Marshal(sessions)
		annotations[v1beta1.DeviceAnnotationRemoteSession] = string(b)
	}
	return &v1beta1.Device{
		Metadata: v1beta1.ObjectMeta{
			Annotations: &annotations,
		},
	}
}

func serialSession(sessionID, appName string) v1beta1.DeviceRemoteSession {
	return v1beta1.DeviceRemoteSession{
		SessionID:   sessionID,
		AppName:     appName,
		ConsoleType: "serial",
	}
}

func TestAppConsoleManager(t *testing.T) {
	t.Run("When the resolver returns an error it should report it via stream metadata and close the stream without running a session", func(t *testing.T) {
		require := require.New(t)

		resolver := &mockResolver{
			err: map[string]error{"my-app": fmt.Errorf("app is not a VM workload")},
		}
		v := setupTestVars(t, resolver)

		v.mockStream()
		v.mockCloseSend()

		sessionID := uuid.New().String()
		device := makeDevice([]v1beta1.DeviceRemoteSession{serialSession(sessionID, "my-app")})
		v.manager.Sync(v.ctx, device)
		v.waitSessions(t)

		require.Eventually(func() bool {
			v.mu.Lock()
			defer v.mu.Unlock()
			return v.closeSendCalled
		}, 2*time.Second, 20*time.Millisecond, "expected CloseSend to be called")

		require.Equal("app is not a VM workload", v.sentMetadataValue(consts.GrpcSessionErrorKey),
			"expected the resolver error to be sent via session-error metadata so the server can fail before protocol selection")
		require.Empty(v.sentMetadataValue(consts.GrpcSelectedProtocolKey),
			"selected-protocol metadata must not be sent when resolution failed")
	})

	t.Run("When the gRPC Stream call fails it should skip the session without panicking", func(t *testing.T) {
		require := require.New(t)

		resolver := &mockResolver{sessions: map[string]Session{"my-vm": noopSession{}}}
		v := setupTestVars(t, resolver)

		v.mockStreamError(fmt.Errorf("connection refused"))

		sessionID := uuid.New().String()
		device := makeDevice([]v1beta1.DeviceRemoteSession{serialSession(sessionID, "my-vm")})
		v.manager.Sync(v.ctx, device)
		v.waitSessions(t)

		require.Empty(v.manager.activeSessions)
	})

	t.Run("When the same session ID appears twice only one session should be started", func(t *testing.T) {
		require := require.New(t)

		resolver := &mockResolver{sessions: map[string]Session{"my-vm": noopSession{}}}
		v := setupTestVars(t, resolver)

		// Only one Stream() call expected.
		v.mockStream()
		v.mockCloseSend()

		sessionID := uuid.New().String()
		device := makeDevice([]v1beta1.DeviceRemoteSession{serialSession(sessionID, "my-vm")})

		v.manager.Sync(v.ctx, device)
		v.waitSessions(t)

		// The second sync finds the session already in inactive list and skips it.
		v.manager.Sync(v.ctx, device)
		v.waitSessions(t)

		v.manager.mu.Lock()
		inactiveCount := len(v.manager.inactiveSessions)
		v.manager.mu.Unlock()
		require.Equal(1, inactiveCount, "expected exactly one inactive session after dedup")
	})

	t.Run("When sync is called with no annotation it should do nothing", func(t *testing.T) {
		require := require.New(t)
		v := setupTestVars(t, nil)

		device := &v1beta1.Device{Metadata: v1beta1.ObjectMeta{}}
		v.manager.Sync(v.ctx, device)
		v.waitSessions(t)
		require.Empty(v.manager.activeSessions)
	})

	t.Run("When AppName is empty it should be skipped", func(t *testing.T) {
		require := require.New(t)
		v := setupTestVars(t, nil)

		annotations := make(map[string]string)
		sessions := []v1beta1.DeviceRemoteSession{
			{SessionID: uuid.New().String(), AppName: "", ConsoleType: "serial"},
		}
		b, _ := json.Marshal(sessions)
		annotations[v1beta1.DeviceAnnotationRemoteSession] = string(b)
		device := &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: &annotations}}
		v.manager.Sync(v.ctx, device)
		v.waitSessions(t)
		require.Empty(v.manager.activeSessions)
	})

	t.Run("When a VM session runs end-to-end it should bridge and terminate on gRPC EOF", func(t *testing.T) {
		serverConn, clientConn := net.Pipe()
		defer serverConn.Close()

		vmSess := NewVMSerialSession("virt-launcher-my-vm-compute", &mockExecStreamer{conn: clientConn}, log.NewPrefixLogger("test"))
		resolver := &mockResolver{sessions: map[string]Session{"my-vm": vmSess}}
		v := setupTestVars(t, resolver)

		v.mockStream()
		v.mockSend()
		v.mockRecv()
		v.mockCloseSend()

		sessionID := uuid.New().String()
		device := makeDevice([]v1beta1.DeviceRemoteSession{serialSession(sessionID, "my-vm")})
		v.manager.Sync(v.ctx, device)

		v.sendEOF()
		v.waitSessions(t)
	})

	t.Run("When the dialFn fails it should send an error over the gRPC stream", func(t *testing.T) {
		require := require.New(t)

		vmSess := NewVMSerialSession("virt-launcher-my-vm-compute", &mockExecStreamer{err: fmt.Errorf("podman exec: no such container")}, log.NewPrefixLogger("test"))
		resolver := &mockResolver{sessions: map[string]Session{"my-vm": vmSess}}
		v := setupTestVars(t, resolver)

		v.mockStream()
		v.mockSend()
		v.mockCloseSend()

		sessionID := uuid.New().String()
		device := makeDevice([]v1beta1.DeviceRemoteSession{serialSession(sessionID, "my-vm")})
		v.manager.Sync(v.ctx, device)
		v.waitSessions(t)

		require.Eventually(func() bool {
			v.mu.Lock()
			defer v.mu.Unlock()
			return v.closeSendCalled
		}, 2*time.Second, 20*time.Millisecond, "expected CloseSend to be called after dial failure")

		require.Contains(v.lastSentError(), "podman exec: no such container", "expected the dial error to be sent via the Error field, not Payload")
	})
}

func TestManagerEvict(t *testing.T) {
	t.Run("When no active session matches it should return false", func(t *testing.T) {
		require := require.New(t)
		v := setupTestVars(t, nil)

		require.False(v.manager.evict("nonexistent"))
	})

	t.Run("When an active session matches it should mark it replaced and cancel its context", func(t *testing.T) {
		require := require.New(t)
		v := setupTestVars(t, nil)

		ctx, cancel := context.WithCancel(context.Background())
		ms := &managedSession{id: "session-1", cancel: cancel}
		require.True(v.manager.add(ms))

		require.True(v.manager.evict("session-1"))
		require.True(ms.replaced.Load())
		select {
		case <-ctx.Done():
		default:
			t.Fatal("expected evict to cancel the session's context")
		}
	})
}

// mockSessionStream bundles a MockRouterService_StreamClient with the plumbing needed to
// drive it independently of any other stream, so tests can run two concurrent sessions (an
// evicted one and its replacement) each with their own Send/Recv/CloseSend behavior. Send()
// rejects once streamCtx is done, mirroring real gRPC client streams: this is what makes the
// test able to catch a session's own cancellation racing its "replaced" notice off the wire.
type mockSessionStream struct {
	stream          *MockRouterService_StreamClient
	recvChan        chan lo.Tuple2[*grpc_v1.StreamResponse, error]
	eofOnce         sync.Once
	mu              sync.Mutex
	sentErrors      []string
	closeSendCalled chan struct{}
	closeOnce       sync.Once
	streamCtx       context.Context
	// ready is closed the first time setStreamCtx runs, i.e. once this mock's Stream() call
	// has actually happened. Tests that need to trigger a takeover only after a specific
	// session's Start() has reached Stream() must wait on this instead of on Manager's
	// activeSessions, which is populated earlier (before ResolveConsole/Stream) and so cannot
	// distinguish "session tracked" from "session's stream call has happened".
	ready     chan struct{}
	readyOnce sync.Once
}

func newMockSessionStream(ctrl *gomock.Controller) *mockSessionStream {
	s := &mockSessionStream{
		stream:          NewMockRouterService_StreamClient(ctrl),
		recvChan:        make(chan lo.Tuple2[*grpc_v1.StreamResponse, error]),
		closeSendCalled: make(chan struct{}),
		ready:           make(chan struct{}),
	}
	s.stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(req *grpc_v1.StreamRequest) error {
		s.mu.Lock()
		ctx := s.streamCtx
		s.mu.Unlock()
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if req.GetError() != "" {
			s.mu.Lock()
			s.sentErrors = append(s.sentErrors, req.GetError())
			s.mu.Unlock()
		}
		return nil
	}).AnyTimes()
	s.stream.EXPECT().Recv().DoAndReturn(func() (*grpc_v1.StreamResponse, error) {
		val, ok := <-s.recvChan
		if !ok {
			return nil, io.EOF
		}
		return val.A, val.B
	}).AnyTimes()
	s.stream.EXPECT().CloseSend().DoAndReturn(func() error {
		s.closeOnce.Do(func() { close(s.closeSendCalled) })
		return nil
	}).AnyTimes()
	return s
}

func (s *mockSessionStream) sendEOF() {
	s.eofOnce.Do(func() { close(s.recvChan) })
}

// setStreamCtx records the outgoing context Stream() was called with, so later Send() calls
// can be rejected once it is done -- exactly as a real gRPC client stream would behave. It
// also signals ready, since this is only ever called from the Stream() mock itself.
func (s *mockSessionStream) setStreamCtx(ctx context.Context) {
	s.mu.Lock()
	s.streamCtx = ctx
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(s.ready) })
}

func (s *mockSessionStream) errors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sentErrors...)
}

func TestSyncReplacesSessionID(t *testing.T) {
	t.Run("When a new entry names an active session via ReplacesSessionID it should evict it and report why", func(t *testing.T) {
		require := require.New(t)
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		logger := log.NewPrefixLogger("test")
		mockGrpcClient := NewMockRouterServiceClient(ctrl)

		oldServerConn, oldClientConn := net.Pipe()
		defer oldServerConn.Close()
		newServerConn, newClientConn := net.Pipe()
		defer newServerConn.Close()

		oldSess := NewVMSerialSession("virt-launcher-my-vm-compute", &mockExecStreamer{conn: oldClientConn}, logger)
		newSess := NewVMSerialSession("virt-launcher-my-vm-compute", &mockExecStreamer{conn: newClientConn}, logger)

		oldSessionID := uuid.New().String()
		newSessionID := uuid.New().String()

		resolver := &mockResolver{seq: map[string][]Session{"my-vm": {oldSess, newSess}}}

		oldStream := newMockSessionStream(ctrl)
		newStream := newMockSessionStream(ctrl)

		var callCount int
		var callMu sync.Mutex
		mockGrpcClient.EXPECT().Stream(gomock.Any()).DoAndReturn(func(ctx context.Context, _ ...grpc.CallOption) (grpc_v1.RouterService_StreamClient, error) {
			callMu.Lock()
			callCount++
			n := callCount
			callMu.Unlock()
			if n == 1 {
				oldStream.setStreamCtx(ctx)
				return oldStream.stream, nil
			}
			newStream.setStreamCtx(ctx)
			return newStream.stream, nil
		}).Times(2)

		manager := NewManager(mockGrpcClient, "test-device", resolver, logger)

		device1 := makeDevice([]v1beta1.DeviceRemoteSession{serialSession(oldSessionID, "my-vm")})
		manager.Sync(context.Background(), device1)

		// Wait for the old session's own Stream() call to have happened (not just for it to be
		// tracked in activeSessions, which happens earlier and races against a new session's
		// Start() also reaching Stream() first, which would bind the mocks to the wrong session).
		select {
		case <-oldStream.ready:
		case <-time.After(2 * time.Second):
			t.Fatal("expected the old session's Stream() call to happen before triggering the takeover")
		}

		device2 := makeDevice([]v1beta1.DeviceRemoteSession{
			{SessionID: newSessionID, AppName: "my-vm", ConsoleType: "serial", ReplacesSessionID: oldSessionID},
		})
		manager.Sync(context.Background(), device2)

		select {
		case <-oldStream.closeSendCalled:
		case <-time.After(2 * time.Second):
			t.Fatal("expected the old session's stream to be closed after eviction")
		}
		require.Contains(oldStream.errors(), "console session replaced by a new connection",
			"the evicted session must report why it was torn down")

		newStream.sendEOF()
		oldStream.sendEOF()
		waitForSessions(t, manager)
	})

	t.Run("When a session's entry simply disappears without ReplacesSessionID it should keep running", func(t *testing.T) {
		require := require.New(t)

		serverConn, clientConn := net.Pipe()
		defer serverConn.Close()
		vmSess := NewVMSerialSession("virt-launcher-my-vm-compute", &mockExecStreamer{conn: clientConn}, log.NewPrefixLogger("test"))
		resolver := &mockResolver{sessions: map[string]Session{"my-vm": vmSess}}
		v := setupTestVars(t, resolver)

		v.mockStream()
		v.mockSend()
		v.mockRecv()
		v.mockCloseSend()

		sessionID := uuid.New().String()
		device := makeDevice([]v1beta1.DeviceRemoteSession{serialSession(sessionID, "my-vm")})
		v.manager.Sync(v.ctx, device)

		require.Eventually(func() bool {
			v.manager.mu.Lock()
			defer v.manager.mu.Unlock()
			return len(v.manager.activeSessions) == 1
		}, 2*time.Second, 10*time.Millisecond, "expected the session to become active")

		// The entry vanishes entirely (e.g. an unrelated close-then-reopen elsewhere in the
		// annotation) rather than being explicitly replaced. This must never tear the session
		// down: only an explicit ReplacesSessionID is a valid eviction signal.
		v.manager.Sync(v.ctx, makeDevice(nil))

		v.mu.Lock()
		closed := v.closeSendCalled
		v.mu.Unlock()
		require.False(closed, "a session must not be evicted just because its entry vanished from the annotation")

		v.sendEOF()
		v.waitSessions(t)
	})
}

func TestVMVNCSession(t *testing.T) {
	t.Run("When the dialFn fails it should send an error over the gRPC stream", func(t *testing.T) {
		require := require.New(t)

		vncSess := NewVMVNCSession("virt-launcher-my-vm-compute", &mockExecStreamer{err: fmt.Errorf("podman exec: no such container")}, log.NewPrefixLogger("test"))
		resolver := &mockResolver{sessions: map[string]Session{"my-vm": vncSess}}
		v := setupTestVars(t, resolver)

		v.mockStream()
		v.mockSend()
		v.mockCloseSend()

		sessionID := uuid.New().String()
		device := makeDevice([]v1beta1.DeviceRemoteSession{
			{SessionID: sessionID, AppName: "my-vm", ConsoleType: "vnc"},
		})
		v.manager.Sync(v.ctx, device)
		v.waitSessions(t)

		require.Eventually(func() bool {
			v.mu.Lock()
			defer v.mu.Unlock()
			return v.closeSendCalled
		}, 2*time.Second, 20*time.Millisecond, "expected CloseSend to be called after dial failure")

		require.Contains(v.lastSentError(), "podman exec: no such container", "expected the dial error to be sent via the Error field, not Payload")
	})

	t.Run("When a VNC session runs end-to-end it should bridge and terminate on gRPC EOF", func(t *testing.T) {
		serverConn, clientConn := net.Pipe()
		defer serverConn.Close()

		vncSess := NewVMVNCSession("virt-launcher-my-vm-compute", &mockExecStreamer{conn: clientConn}, log.NewPrefixLogger("test"))
		resolver := &mockResolver{sessions: map[string]Session{"my-vm": vncSess}}
		v := setupTestVars(t, resolver)

		v.mockStream()
		v.mockSend()
		v.mockRecv()
		v.mockCloseSend()

		sessionID := uuid.New().String()
		device := makeDevice([]v1beta1.DeviceRemoteSession{
			{SessionID: sessionID, AppName: "my-vm", ConsoleType: "vnc"},
		})
		v.manager.Sync(v.ctx, device)

		v.sendEOF()
		v.waitSessions(t)
	})

	t.Run("When a VNC session starts it should not send an initial CR", func(t *testing.T) {
		require := require.New(t)

		// VNC uses the binary RFB protocol; an unsolicited CR would corrupt the handshake.
		// This test verifies Run() does not write anything to the connection before
		// the gRPC stream sends data.
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		logger := log.NewPrefixLogger("test")
		mockStreamClient := NewMockRouterService_StreamClient(ctrl)

		serverConn, clientConn := net.Pipe()

		type readResult struct {
			n   int
			err error
		}
		readDone := make(chan readResult, 1)
		go func() {
			// require/t.FailNow must only be called from the test's main goroutine, so
			// errors here are reported via readDone instead of require calls.
			buf := make([]byte, 1)
			if err := serverConn.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
				// Run()'s teardown may have already closed the other end of the pipe
				// (clientConn), which net.Pipe surfaces here too. That race is fine: it
				// still proves zero bytes were written before teardown.
				readDone <- readResult{n: 0, err: err}
				serverConn.Close()
				return
			}
			n, err := serverConn.Read(buf)
			readDone <- readResult{n: n, err: err}
			serverConn.Close()
		}()

		// EOF terminates the session immediately.
		mockStreamClient.EXPECT().Recv().Return(nil, io.EOF).AnyTimes()
		mockStreamClient.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
		mockStreamClient.EXPECT().CloseSend().Return(nil).AnyTimes()

		vncSess := NewVMVNCSession("virt-launcher-my-vm-compute", &mockExecStreamer{conn: clientConn}, logger)
		vncSess.Run(context.Background(), mockStreamClient)

		var result readResult
		select {
		case result = <-readDone:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("timed out waiting for serverConn.Read to complete")
		}

		// No data should ever have reached serverConn: either the deadline expires (no write
		// happened) or Run()'s teardown closes the connection first (also yielding an error,
		// e.g. io.EOF) -- both indicate zero bytes were written. Only a successful read with
		// n > 0 indicates the bug (an unsolicited byte was written).
		require.Equal(0, result.n, "vm vnc session must not write an initial byte before the gRPC stream sends data")
		require.Error(result.err, "expected the read to fail (deadline exceeded or connection closed) since no data should have been written")
	})
}

func TestSyncMalformedAnnotation(t *testing.T) {
	t.Run("When the remote session annotation is malformed JSON it should skip gracefully", func(t *testing.T) {
		require := require.New(t)
		v := setupTestVars(t, nil)

		annotations := map[string]string{
			v1beta1.DeviceAnnotationRemoteSession: "not-valid-json",
		}
		device := &v1beta1.Device{
			Metadata: v1beta1.ObjectMeta{Annotations: &annotations},
		}
		v.manager.Sync(v.ctx, device)
		v.waitSessions(t)

		require.Empty(v.manager.activeSessions)
	})
}

func TestSessionBridgeErrorPaths(t *testing.T) {
	t.Run("When gRPC Send fails it should terminate the bridge", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		logger := log.NewPrefixLogger("test")
		mockStreamClient := NewMockRouterService_StreamClient(ctrl)

		serverConn, clientConn := net.Pipe()
		defer serverConn.Close()

		mockStreamClient.EXPECT().Recv().Return(nil, io.EOF).AnyTimes()
		mockStreamClient.EXPECT().Send(gomock.Any()).Return(fmt.Errorf("send failed")).AnyTimes()
		mockStreamClient.EXPECT().CloseSend().Return(nil)

		bridgeDone := make(chan struct{})
		go func() {
			defer close(bridgeDone)
			bridgeConn(context.Background(), "serial", clientConn, mockStreamClient, logger)
		}()

		_, _ = serverConn.Write([]byte("hello"))

		select {
		case <-bridgeDone:
		case <-time.After(2 * time.Second):
			t.Fatal("bridge did not terminate after gRPC Send failure")
		}
	})

	t.Run("When socket Write fails it should terminate the bridge", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		logger := log.NewPrefixLogger("test")
		mockStreamClient := NewMockRouterService_StreamClient(ctrl)

		serverConn, clientConn := net.Pipe()

		recvChan := make(chan lo.Tuple2[*grpc_v1.StreamResponse, error], 1)
		recvChan <- lo.T2[*grpc_v1.StreamResponse, error](&grpc_v1.StreamResponse{Payload: []byte("data")}, nil)
		serverConn.Close()

		mockStreamClient.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
		mockStreamClient.EXPECT().Recv().DoAndReturn(func() (*grpc_v1.StreamResponse, error) {
			val, ok := <-recvChan
			if !ok {
				return nil, io.EOF
			}
			return val.A, val.B
		}).AnyTimes()
		mockStreamClient.EXPECT().CloseSend().Return(nil)

		bridgeDone := make(chan struct{})
		go func() {
			defer close(bridgeDone)
			bridgeConn(context.Background(), "serial", clientConn, mockStreamClient, logger)
		}()

		select {
		case <-bridgeDone:
		case <-time.After(2 * time.Second):
			t.Fatal("bridge did not terminate after socket Write failure")
		}
	})

	t.Run("When the gRPC stream reaches EOF it should close the socket connection", func(t *testing.T) {
		serverConn, clientConn := net.Pipe()
		defer serverConn.Close()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		logger := log.NewPrefixLogger("test")
		mockStreamClient := NewMockRouterService_StreamClient(ctrl)

		recvChan := make(chan lo.Tuple2[*grpc_v1.StreamResponse, error])
		mockStreamClient.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
		mockStreamClient.EXPECT().Recv().DoAndReturn(func() (*grpc_v1.StreamResponse, error) {
			val, ok := <-recvChan
			if !ok {
				return nil, io.EOF
			}
			return val.A, val.B
		}).AnyTimes()
		mockStreamClient.EXPECT().CloseSend().Return(nil)

		bridgeDone := make(chan struct{})
		go func() {
			defer close(bridgeDone)
			bridgeConn(context.Background(), "serial", clientConn, mockStreamClient, logger)
		}()

		close(recvChan)

		select {
		case <-bridgeDone:
		case <-time.After(2 * time.Second):
			t.Fatal("bridge did not terminate after gRPC EOF")
		}
	})
}
