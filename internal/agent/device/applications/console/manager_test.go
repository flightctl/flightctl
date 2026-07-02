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
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
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
	sentPayloads     [][]byte
	mu               sync.Mutex
	once             sync.Once
	recvChan         chan lo.Tuple2[*grpc_v1.StreamResponse, error]
	closeSendCalled  bool
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
	v.mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(v.mockStreamClient, nil)
}

func (v *testVars) mockStreamError(err error) {
	v.mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(nil, err)
}

func (v *testVars) mockSend() {
	v.mockStreamClient.EXPECT().Send(gomock.Any()).DoAndReturn(func(req *grpc_v1.StreamRequest) error {
		v.mu.Lock()
		v.sentPayloads = append(v.sentPayloads, req.Payload)
		v.mu.Unlock()
		return nil
	}).AnyTimes()
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
	t.Run("When the resolver returns an error it should send the error over the stream and close it", func(t *testing.T) {
		require := require.New(t)

		resolver := &mockResolver{
			err: map[string]error{"my-app": fmt.Errorf("app is not a VM workload")},
		}
		v := setupTestVars(t, resolver)

		v.mockStream()
		v.mockSend()
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

		v.mu.Lock()
		payloads := v.sentPayloads
		v.mu.Unlock()
		require.NotEmpty(payloads, "expected an error message to be sent over gRPC")
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

		v.mu.Lock()
		payloads := v.sentPayloads
		v.mu.Unlock()
		require.NotEmpty(payloads, "expected error payload after dial failure")
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
