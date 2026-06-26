package remote_access_server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/sirupsen/logrus"
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
