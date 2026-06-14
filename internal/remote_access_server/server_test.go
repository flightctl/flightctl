package remote_access_server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// stubStreamServer is a hand-written stub for pb.RouterService_StreamServer.
type stubStreamServer struct {
	grpc.ServerStream
}

func (s *stubStreamServer) Send(*pb.StreamResponse) error    { return nil }
func (s *stubStreamServer) Recv() (*pb.StreamRequest, error) { return nil, nil }
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

func TestServerStream(t *testing.T) {
	t.Parallel()
	srv := &Server{}
	stream := &stubStreamServer{}

	err := srv.Stream(stream)
	if err != nil {
		t.Errorf("When Stream is called it should return nil, got %v", err)
	}
}
