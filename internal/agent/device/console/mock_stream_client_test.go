package console

import (
	"context"
	"reflect"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	gomock "go.uber.org/mock/gomock"
	"google.golang.org/grpc/metadata"
)

// MockRouterService_StreamClient is a mock of the RouterService_StreamClient
// (grpc.BidiStreamingClient[StreamRequest, StreamResponse]) interface.
// Hand-written because protoc-gen-go-grpc v1.6+ generates generic type aliases
// that mockgen does not produce dedicated stream mocks for.
type MockRouterService_StreamClient struct {
	ctrl     *gomock.Controller
	recorder *MockRouterService_StreamClientMockRecorder
}

// MockRouterService_StreamClientMockRecorder is the mock recorder for MockRouterService_StreamClient.
type MockRouterService_StreamClientMockRecorder struct {
	mock *MockRouterService_StreamClient
}

// NewMockRouterService_StreamClient creates a new mock instance.
func NewMockRouterService_StreamClient(ctrl *gomock.Controller) *MockRouterService_StreamClient {
	mock := &MockRouterService_StreamClient{ctrl: ctrl}
	mock.recorder = &MockRouterService_StreamClientMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockRouterService_StreamClient) EXPECT() *MockRouterService_StreamClientMockRecorder {
	return m.recorder
}

// Send mocks base method.
func (m *MockRouterService_StreamClient) Send(req *grpc_v1.StreamRequest) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Send", req)
	ret0, _ := ret[0].(error)
	return ret0
}

// Send indicates an expected call of Send.
func (mr *MockRouterService_StreamClientMockRecorder) Send(req any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Send", reflect.TypeOf((*MockRouterService_StreamClient)(nil).Send), req)
}

// Recv mocks base method.
func (m *MockRouterService_StreamClient) Recv() (*grpc_v1.StreamResponse, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Recv")
	ret0, _ := ret[0].(*grpc_v1.StreamResponse)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Recv indicates an expected call of Recv.
func (mr *MockRouterService_StreamClientMockRecorder) Recv() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Recv", reflect.TypeOf((*MockRouterService_StreamClient)(nil).Recv))
}

// CloseSend mocks base method.
func (m *MockRouterService_StreamClient) CloseSend() error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CloseSend")
	ret0, _ := ret[0].(error)
	return ret0
}

// CloseSend indicates an expected call of CloseSend.
func (mr *MockRouterService_StreamClientMockRecorder) CloseSend() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CloseSend", reflect.TypeOf((*MockRouterService_StreamClient)(nil).CloseSend))
}

// Header mocks base method.
func (m *MockRouterService_StreamClient) Header() (metadata.MD, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Header")
	ret0, _ := ret[0].(metadata.MD)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Header indicates an expected call of Header.
func (mr *MockRouterService_StreamClientMockRecorder) Header() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Header", reflect.TypeOf((*MockRouterService_StreamClient)(nil).Header))
}

// Trailer mocks base method.
func (m *MockRouterService_StreamClient) Trailer() metadata.MD {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Trailer")
	ret0, _ := ret[0].(metadata.MD)
	return ret0
}

// Trailer indicates an expected call of Trailer.
func (mr *MockRouterService_StreamClientMockRecorder) Trailer() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Trailer", reflect.TypeOf((*MockRouterService_StreamClient)(nil).Trailer))
}

// Context mocks base method.
func (m *MockRouterService_StreamClient) Context() context.Context {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Context")
	ret0, _ := ret[0].(context.Context)
	return ret0
}

// Context indicates an expected call of Context.
func (mr *MockRouterService_StreamClientMockRecorder) Context() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Context", reflect.TypeOf((*MockRouterService_StreamClient)(nil).Context))
}

// SendMsg mocks base method.
func (m *MockRouterService_StreamClient) SendMsg(msg any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendMsg", msg)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendMsg indicates an expected call of SendMsg.
func (mr *MockRouterService_StreamClientMockRecorder) SendMsg(msg any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendMsg", reflect.TypeOf((*MockRouterService_StreamClient)(nil).SendMsg), msg)
}

// RecvMsg mocks base method.
func (m *MockRouterService_StreamClient) RecvMsg(msg any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RecvMsg", msg)
	ret0, _ := ret[0].(error)
	return ret0
}

// RecvMsg indicates an expected call of RecvMsg.
func (mr *MockRouterService_StreamClientMockRecorder) RecvMsg(msg any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RecvMsg", reflect.TypeOf((*MockRouterService_StreamClient)(nil).RecvMsg), msg)
}
