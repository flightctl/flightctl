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

// mockResolver is a minimal AppConsoleResolver for tests.
type mockResolver struct {
	sessions map[string]Session
	err      map[string]error
}

func (m *mockResolver) ResolveConsole(appName, _ string) (Session, error) {
	if err, ok := m.err[appName]; ok {
		return nil, err
	}
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
		v.manager.sessionWg.Wait()

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
		v.manager.sessionWg.Wait()

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
		v.manager.sessionWg.Wait()

		// The second sync finds the session already in inactive list and skips it.
		v.manager.Sync(v.ctx, device)
		v.manager.sessionWg.Wait()

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
		v.manager.sessionWg.Wait()
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
		v.manager.sessionWg.Wait()
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
		v.manager.sessionWg.Wait()
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
		v.manager.sessionWg.Wait()

		require.Eventually(func() bool {
			v.mu.Lock()
			defer v.mu.Unlock()
			return v.closeSendCalled
		}, 2*time.Second, 20*time.Millisecond, "expected CloseSend to be called after dial failure")

		require.Contains(v.lastSentError(), "podman exec: no such container", "expected the dial error to be sent via the Error field, not Payload")
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
		v.manager.sessionWg.Wait()

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
		v.manager.sessionWg.Wait()
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
			buf := make([]byte, 1)
			require.NoError(serverConn.SetDeadline(time.Now().Add(100 * time.Millisecond)))
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
		v.manager.sessionWg.Wait()

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
