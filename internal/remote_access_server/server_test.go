package remote_access_server

import (
	"context"
	"io"
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

// stubStreamServer is a hand-written stub for pb.RouterService_StreamServer.
type stubStreamServer struct {
	grpc.ServerStream
}

func (s *stubStreamServer) Send(*pb.StreamResponse) error    { return nil }
func (s *stubStreamServer) Recv() (*pb.StreamRequest, error) { return nil, io.EOF }
func (s *stubStreamServer) Context() context.Context         { return context.Background() }
func (s *stubStreamServer) SendMsg(interface{}) error        { return nil }
func (s *stubStreamServer) RecvMsg(interface{}) error        { return nil }
func (s *stubStreamServer) SetHeader(metadata.MD) error      { return nil }
func (s *stubStreamServer) SendHeader(metadata.MD) error     { return nil }
func (s *stubStreamServer) SetTrailer(metadata.MD)           {}

func TestStubHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "GET root", method: http.MethodGet, path: "/"},
		{name: "POST api path", method: http.MethodPost, path: "/api/v1/something"},
		{name: "PUT arbitrary path", method: http.MethodPut, path: "/ws/v1/devices/foo/console"},
		{name: "DELETE", method: http.MethodDelete, path: "/any"},
	}

	handler := stubHandler()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotImplemented {
				t.Errorf("When %s %s it should return 501, got %d", tc.method, tc.path, rec.Code)
			}
		})
	}
}

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

func TestServerStream(t *testing.T) {
	t.Parallel()
	srv := &Server{}
	stream := &stubStreamServer{}

	err := srv.Stream(stream)
	if err == nil {
		t.Fatal("When Stream is called it should return an error, got nil")
	}
	if code := status.Code(err); code != codes.Unavailable {
		t.Errorf("When Stream is called it should return codes.Unavailable, got %v", code)
	}
}
