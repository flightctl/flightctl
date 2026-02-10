package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type lockBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (l *lockBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.Write(p)
}

func (l *lockBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.String()
}

type vars struct {
	ctx              context.Context
	controller       *Manager
	ctrl             *gomock.Controller
	mockGrpcClient   *MockRouterServiceClient
	mockStreamClient *MockRouterService_StreamClient
	mockWatcher      *spec.MockWatcher
	executor         executer.Executer
	logger           *log.PrefixLogger
	recvChan         chan lo.Tuple2[*grpc_v1.StreamResponse, error]
	stdoutBuffer     lockBuffer
	stderrBuffer     lockBuffer
	errBuffer        lockBuffer
	once             sync.Once
}

func setupVars(t *testing.T) *vars {
	ctrl := gomock.NewController(t)
	executor := executer.NewCommonExecuter()
	logger := log.NewPrefixLogger("console")
	logger.SetLevel(logrus.DebugLevel)
	mockGrpcClient := NewMockRouterServiceClient(ctrl)
	mockStreamClient := NewMockRouterService_StreamClient(ctrl)
	mockWatcher := spec.NewMockWatcher(ctrl)

	v := &vars{
		ctx:              context.Background(),
		ctrl:             ctrl,
		mockGrpcClient:   mockGrpcClient,
		mockStreamClient: mockStreamClient,
		mockWatcher:      mockWatcher,
		executor:         executor,
		logger:           logger,
		controller: NewManager(
			mockGrpcClient,
			"mydevice",
			executor,
			mockWatcher,
			logger),
		recvChan: make(chan lo.Tuple2[*grpc_v1.StreamResponse, error]),
	}

	t.Cleanup(func() { ctrl.Finish() }) // Equivalent to AfterEach
	return v
}

func sessionMetadata(t *testing.T, term string, initialDimensions *v1beta1.TerminalSize, command *v1beta1.DeviceCommand, tty bool) string {
	metadata := v1beta1.DeviceConsoleSessionMetadata{
		Term:              lo.Ternary(term != "", &term, nil),
		InitialDimensions: initialDimensions,
		Command:           command,
		TTY:               tty,
		Protocols: []string{
			StreamProtocolV5Name,
		},
	}
	b, err := json.Marshal(&metadata)
	require.Nil(t, err)
	return string(b)
}

func deviceConsole(id string, sessionMetadata string) v1beta1.DeviceConsole {
	return v1beta1.DeviceConsole{
		SessionID:       id,
		SessionMetadata: sessionMetadata,
	}
}

func desiredSpec(consoles ...v1beta1.DeviceConsole) *v1beta1.DeviceSpec {
	return &v1beta1.DeviceSpec{
		Consoles: &consoles,
	}
}

func mockStream(v *vars) {
	v.mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(v.mockStreamClient, nil)
}

func mockSend(v *vars, times int) {
	m := v.mockStreamClient.EXPECT().Send(gomock.Any()).DoAndReturn(
		func(req *grpc_v1.StreamRequest) error {
			if req == nil || len(req.Payload) == 0 {
				return errors.New("unexpected nil request")
			}
			switch req.Payload[0] {
			case StdoutID:
				_, _ = v.stdoutBuffer.Write(req.Payload[1:])
			case StderrID:
				_, _ = v.stderrBuffer.Write(req.Payload[1:])
			case ErrID:
				_, _ = v.errBuffer.Write(req.Payload[1:])
			default:
				return errors.New("unexpected payload prefix")
			}
			return nil
		})
	if times > 0 {
		m.Times(times)
	} else {
		m.AnyTimes()
	}
}

func mockRecv(v *vars) {
	v.mockStreamClient.EXPECT().Recv().DoAndReturn(func() (*grpc_v1.StreamResponse, error) {
		val, ok := <-v.recvChan
		if ok {
			return val.A, val.B
		}
		return nil, io.EOF
	}).AnyTimes()
}

func mockCloseSend(v *vars) {
	v.mockStreamClient.EXPECT().CloseSend().DoAndReturn(func() error {
		v.once.Do(func() {
			close(v.recvChan)
		})
		return nil
	}).AnyTimes()
}

func sendInput(v *vars, id byte, b []byte) {
	v.recvChan <- lo.Tuple2[*grpc_v1.StreamResponse, error]{
		A: &grpc_v1.StreamResponse{
			Payload: append([]byte{id}, b...),
		},
	}
}

func TestConsole(t *testing.T) {
	t.Run("no process created", func(t *testing.T) {
		v := setupVars(t)
		v.controller.sync(v.ctx, desiredSpec())
	})

	t.Run("create process and close without tty", func(t *testing.T) {
		v := setupVars(t)
		sessionID := uuid.New().String()
		consoleDef := deviceConsole(sessionID, sessionMetadata(t, "xterm", nil,
			&v1beta1.DeviceCommand{
				Command: "echo",
				Args: []string{
					"hello world",
				}},
			false))

		mockStream(v)
		mockCloseSend(v)
		mockSend(v, 2)
		mockRecv(v)

		v.controller.sync(v.ctx, desiredSpec(consoleDef))

		require.Eventually(t, func() bool {
			return bytes.Contains([]byte(v.stdoutBuffer.String()), []byte("hello world"))
		}, 2*time.Second, 50*time.Millisecond, "Expected stdout to contain 'hello world'")
		require.Eventually(t, func() bool {
			return bytes.Contains([]byte(v.errBuffer.String()), []byte(`Success`))
		}, 2*time.Second, 50*time.Millisecond, "Expected error to contain 'Success'")
	})

	t.Run("support exit code without tty", func(t *testing.T) {
		v := setupVars(t)
		sessionID := uuid.New().String()
		consoleDef := deviceConsole(sessionID, sessionMetadata(t, "xterm", nil,
			&v1beta1.DeviceCommand{
				Command: "exit",
				Args: []string{
					"11",
				}},
			false))

		mockStream(v)
		mockCloseSend(v)
		mockSend(v, 1)
		mockRecv(v)

		v.controller.sync(v.ctx, desiredSpec(consoleDef))

		require.Eventually(t, func() bool {
			return bytes.Contains([]byte(v.errBuffer.String()), []byte(`"code":11`))
		}, 2*time.Second, 50*time.Millisecond, "Expected error buffer to contain exit code 11")
	})

	t.Run("pipe from stdin with sed replace without tty", func(t *testing.T) {
		v := setupVars(t)
		sessionID := uuid.New().String()
		consoleDef := deviceConsole(sessionID, sessionMetadata(t, "xterm", nil,
			&v1beta1.DeviceCommand{
				Command: "sed",
				Args: []string{
					"s/before/after/"},
			},
			false))

		mockStream(v)
		mockCloseSend(v)
		mockSend(v, 2)
		mockRecv(v)

		v.controller.sync(v.ctx, desiredSpec(consoleDef))

		sendInput(v, StdinID, []byte("before\n"))
		sendInput(v, CloseID, []byte{StdinID})

		require.Eventually(t, func() bool {
			return bytes.Contains([]byte(v.stdoutBuffer.String()), []byte("after"))
		}, 2*time.Second, 50*time.Millisecond, "Expected stdout to contain 'after'")
		require.Eventually(t, func() bool {
			return bytes.Contains([]byte(v.errBuffer.String()), []byte(`Success`))
		}, 2*time.Second, 50*time.Millisecond, "Expected error to contain 'Success'")
	})

	t.Run("separate stdout and stderr without tty", func(t *testing.T) {
		v := setupVars(t)
		sessionID := uuid.New().String()
		consoleDef := deviceConsole(sessionID, sessionMetadata(t, "xterm", nil, &v1beta1.DeviceCommand{
			Command: "echo",
			Args: []string{"stdout",
				";",
				"echo",
				"stderr",
				">&2",
			},
		}, false))

		mockStream(v)
		mockCloseSend(v)
		mockSend(v, 3)
		mockRecv(v)

		v.controller.sync(v.ctx, desiredSpec(consoleDef))

		require.Eventually(t, func() bool {
			return bytes.Contains([]byte(v.stdoutBuffer.String()), []byte("stdout"))
		}, 2*time.Second, 50*time.Millisecond, "Expected stdout to contain 'stdout'")
		require.Eventually(t, func() bool {
			return bytes.Contains([]byte(v.stderrBuffer.String()), []byte("stderr"))
		}, 2*time.Second, 50*time.Millisecond, "Expected stderr to contain 'stderr'")
		require.Eventually(t, func() bool {
			return bytes.Contains([]byte(v.errBuffer.String()), []byte(`Success`))
		}, 2*time.Second, 50*time.Millisecond, "Expected error to contain 'Success'")
	})

	t.Run("echo stdin with tty", func(t *testing.T) {
		v := setupVars(t)
		sessionID := uuid.New().String()
		consoleDef := deviceConsole(sessionID, sessionMetadata(t, "xterm", &v1beta1.TerminalSize{
			Width:  256,
			Height: 50,
		}, nil, true))

		mockStream(v)
		mockCloseSend(v)
		mockRecv(v)
		mockSend(v, 0)

		v.controller.sync(v.ctx, desiredSpec(consoleDef))

		require.Eventually(t, func() bool {
			return v.stdoutBuffer.String() != ""
		}, 3*time.Second, 50*time.Millisecond, "Expected to get bash prompt")

		sendInput(v, StdinID, []byte("echo hello world"))

		require.Eventually(t, func() bool {
			return strings.Contains(v.stdoutBuffer.String(), "echo hello world")
		}, 2*time.Second, 50*time.Millisecond, "Expected stdout to contain 'hello world' got %s", &v.stdoutBuffer)

		require.Equal(t, v.errBuffer.String(), "")

		sendInput(v, StdinID, []byte("\nexit\n"))

		require.Eventually(t, func() bool {
			return v.errBuffer.String() != ""
		}, 2*time.Second, 50*time.Millisecond, "Expected the process to exit")
	})
}
