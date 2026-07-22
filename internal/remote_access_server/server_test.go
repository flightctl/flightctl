package remote_access_server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// stubStreamServer is a minimal implementation of pb.RouterService_StreamServer
// used in unit tests that do not need real transport behaviour.
type stubStreamServer struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *stubStreamServer) Send(*pb.StreamResponse) error    { return nil }
func (s *stubStreamServer) Recv() (*pb.StreamRequest, error) { return nil, nil }
func (s *stubStreamServer) Context() context.Context         { return s.ctx }
func (s *stubStreamServer) SendMsg(interface{}) error        { return nil }
func (s *stubStreamServer) RecvMsg(interface{}) error        { return nil }
func (s *stubStreamServer) SetHeader(metadata.MD) error      { return nil }
func (s *stubStreamServer) SendHeader(metadata.MD) error     { return nil }
func (s *stubStreamServer) SetTrailer(metadata.MD)           {}

func TestGrpcMuxHandlerFunc(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		contentType     string
		proto           int
		expectHTTPRoute bool
	}{
		{name: "When gRPC exact content-type it should not route to HTTP stub", contentType: "application/grpc", proto: 2, expectHTTPRoute: false},
		{name: "When application/grpc+proto it should not route to HTTP stub", contentType: "application/grpc+proto", proto: 2, expectHTTPRoute: false},
		{name: "When application/grpc with params it should not route to HTTP stub", contentType: "application/grpc; charset=utf-8", proto: 2, expectHTTPRoute: false},
		{name: "When HTTP/1 with gRPC content-type it should route to HTTP stub", contentType: "application/grpc", proto: 1, expectHTTPRoute: true},
		{name: "When non-gRPC content-type it should route to HTTP stub", contentType: "application/json", proto: 2, expectHTTPRoute: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var httpHit bool
			sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				httpHit = true
				w.WriteHeader(http.StatusNotImplemented)
			})

			log := logrus.New()
			handler := grpcMuxHandlerFunc(grpc.NewServer(), sentinel, log)

			req := httptest.NewRequest(http.MethodPost, "/api.RouterService/Stream", nil)
			req.ProtoMajor = tc.proto
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if tc.expectHTTPRoute && !httpHit {
				t.Errorf("expected HTTP stub to be called for Content-Type %q HTTP/%d", tc.contentType, tc.proto)
			}
			if !tc.expectHTTPRoute && httpHit {
				t.Errorf("expected gRPC routing for Content-Type %q HTTP/%d, but HTTP stub was called", tc.contentType, tc.proto)
			}
		})
	}
}

func TestServerStream_MissingMetadata(t *testing.T) {
	t.Parallel()
	srv := &Server{}
	stream := &stubStreamServer{ctx: context.Background()}

	err := srv.Stream(stream)
	if err == nil {
		t.Fatal("When Stream is called without metadata it should return an error")
	}
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", code)
	}
}

func TestServerStream_MissingSessionID(t *testing.T) {
	t.Parallel()
	srv := &Server{}
	md := metadata.New(map[string]string{})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	stream := &stubStreamServer{ctx: ctx}

	err := srv.Stream(stream)
	if err == nil {
		t.Fatal("When Stream is called without session ID it should return an error")
	}
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", code)
	}
}

// scriptedStreamServer is a stubStreamServer whose Recv() replays a fixed
// sequence of StreamRequest messages, then blocks until the test's context
// is cancelled (mimicking a live gRPC stream that has nothing left to say).
type scriptedStreamServer struct {
	stubStreamServer
	mu   sync.Mutex
	msgs []*pb.StreamRequest
	idx  int
}

func (s *scriptedStreamServer) Recv() (*pb.StreamRequest, error) {
	s.mu.Lock()
	if s.idx < len(s.msgs) {
		msg := s.msgs[s.idx]
		s.idx++
		s.mu.Unlock()
		return msg, nil
	}
	s.mu.Unlock()
	<-s.ctx.Done()
	return nil, io.EOF
}

func TestPipeStreamToChannel_AgentError_RoutesToErrChNotPayloadCh(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream := &scriptedStreamServer{
		stubStreamServer: stubStreamServer{ctx: ctx},
		msgs: []*pb.StreamRequest{
			{Payload: []byte("hello")},
			{Error: "app is not a VM workload"},
		},
	}

	srv := &Server{log: logrus.New()}
	ch := make(chan []byte, 4)
	errCh := make(chan string, 1)

	done := make(chan struct{})
	go func() {
		srv.pipeStreamToChannel(ctx, stream, ch, errCh)
		close(done)
	}()

	// The leading payload message must still be forwarded on ch before the error.
	select {
	case payload := <-ch:
		assert.Equal(t, []byte("hello"), payload)
	case <-time.After(time.Second):
		t.Fatal("expected the payload preceding the error to be forwarded on ch")
	}

	select {
	case agentErr := <-errCh:
		assert.Equal(t, "app is not a VM workload", agentErr)
	case <-time.After(time.Second):
		t.Fatal("expected the agent error to be forwarded on errCh")
	}

	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond, "expected pipeStreamToChannel to return after reporting the error")

	// ch must be left open (never closed) on the error path so a consumer racing on
	// a select over both channels cannot observe a spurious close instead of the error.
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("ch must not be closed on the agent-error path")
		}
		t.Fatal("unexpected additional payload on ch")
	default:
	}
}
