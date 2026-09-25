package grpc_v1

import (
	"context"
	"reflect"

	gomock "go.uber.org/mock/gomock"
	"google.golang.org/grpc/metadata"
)

// MockEnrollment_TPMChallengeServer is a mock of the Enrollment_TPMChallengeServer
// (grpc.BidiStreamingServer[AgentChallenge, ServerChallenge]) interface.
// Hand-written because protoc-gen-go-grpc v1.6+ generates generic type aliases
// that mockgen does not produce dedicated stream mocks for.
type MockEnrollment_TPMChallengeServer struct {
	ctrl     *gomock.Controller
	recorder *MockEnrollment_TPMChallengeServerMockRecorder
}

// MockEnrollment_TPMChallengeServerMockRecorder is the mock recorder for MockEnrollment_TPMChallengeServer.
type MockEnrollment_TPMChallengeServerMockRecorder struct {
	mock *MockEnrollment_TPMChallengeServer
}

// NewMockEnrollment_TPMChallengeServer creates a new mock instance.
func NewMockEnrollment_TPMChallengeServer(ctrl *gomock.Controller) *MockEnrollment_TPMChallengeServer {
	mock := &MockEnrollment_TPMChallengeServer{ctrl: ctrl}
	mock.recorder = &MockEnrollment_TPMChallengeServerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockEnrollment_TPMChallengeServer) EXPECT() *MockEnrollment_TPMChallengeServerMockRecorder {
	return m.recorder
}

// Send mocks base method.
func (m *MockEnrollment_TPMChallengeServer) Send(resp *ServerChallenge) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Send", resp)
	ret0, _ := ret[0].(error)
	return ret0
}

// Send indicates an expected call of Send.
func (mr *MockEnrollment_TPMChallengeServerMockRecorder) Send(resp any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Send", reflect.TypeOf((*MockEnrollment_TPMChallengeServer)(nil).Send), resp)
}

// Recv mocks base method.
func (m *MockEnrollment_TPMChallengeServer) Recv() (*AgentChallenge, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Recv")
	ret0, _ := ret[0].(*AgentChallenge)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Recv indicates an expected call of Recv.
func (mr *MockEnrollment_TPMChallengeServerMockRecorder) Recv() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Recv", reflect.TypeOf((*MockEnrollment_TPMChallengeServer)(nil).Recv))
}

// Context mocks base method.
func (m *MockEnrollment_TPMChallengeServer) Context() context.Context {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Context")
	ret0, _ := ret[0].(context.Context)
	return ret0
}

// Context indicates an expected call of Context.
func (mr *MockEnrollment_TPMChallengeServerMockRecorder) Context() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Context", reflect.TypeOf((*MockEnrollment_TPMChallengeServer)(nil).Context))
}

// SetHeader mocks base method.
func (m *MockEnrollment_TPMChallengeServer) SetHeader(md metadata.MD) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SetHeader", md)
	ret0, _ := ret[0].(error)
	return ret0
}

// SetHeader indicates an expected call of SetHeader.
func (mr *MockEnrollment_TPMChallengeServerMockRecorder) SetHeader(md any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetHeader", reflect.TypeOf((*MockEnrollment_TPMChallengeServer)(nil).SetHeader), md)
}

// SendHeader mocks base method.
func (m *MockEnrollment_TPMChallengeServer) SendHeader(md metadata.MD) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendHeader", md)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendHeader indicates an expected call of SendHeader.
func (mr *MockEnrollment_TPMChallengeServerMockRecorder) SendHeader(md any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendHeader", reflect.TypeOf((*MockEnrollment_TPMChallengeServer)(nil).SendHeader), md)
}

// SetTrailer mocks base method.
func (m *MockEnrollment_TPMChallengeServer) SetTrailer(md metadata.MD) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "SetTrailer", md)
}

// SetTrailer indicates an expected call of SetTrailer.
func (mr *MockEnrollment_TPMChallengeServerMockRecorder) SetTrailer(md any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetTrailer", reflect.TypeOf((*MockEnrollment_TPMChallengeServer)(nil).SetTrailer), md)
}

// SendMsg mocks base method.
func (m *MockEnrollment_TPMChallengeServer) SendMsg(msg any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendMsg", msg)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendMsg indicates an expected call of SendMsg.
func (mr *MockEnrollment_TPMChallengeServerMockRecorder) SendMsg(msg any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendMsg", reflect.TypeOf((*MockEnrollment_TPMChallengeServer)(nil).SendMsg), msg)
}

// RecvMsg mocks base method.
func (m *MockEnrollment_TPMChallengeServer) RecvMsg(msg any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RecvMsg", msg)
	ret0, _ := ret[0].(error)
	return ret0
}

// RecvMsg indicates an expected call of RecvMsg.
func (mr *MockEnrollment_TPMChallengeServerMockRecorder) RecvMsg(msg any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RecvMsg", reflect.TypeOf((*MockEnrollment_TPMChallengeServer)(nil).RecvMsg), msg)
}

// MockEnrollment_TPMChallengeClient is a mock of the Enrollment_TPMChallengeClient
// (grpc.BidiStreamingClient[AgentChallenge, ServerChallenge]) interface.
type MockEnrollment_TPMChallengeClient struct {
	ctrl     *gomock.Controller
	recorder *MockEnrollment_TPMChallengeClientMockRecorder
}

// MockEnrollment_TPMChallengeClientMockRecorder is the mock recorder for MockEnrollment_TPMChallengeClient.
type MockEnrollment_TPMChallengeClientMockRecorder struct {
	mock *MockEnrollment_TPMChallengeClient
}

// NewMockEnrollment_TPMChallengeClient creates a new mock instance.
func NewMockEnrollment_TPMChallengeClient(ctrl *gomock.Controller) *MockEnrollment_TPMChallengeClient {
	mock := &MockEnrollment_TPMChallengeClient{ctrl: ctrl}
	mock.recorder = &MockEnrollment_TPMChallengeClientMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockEnrollment_TPMChallengeClient) EXPECT() *MockEnrollment_TPMChallengeClientMockRecorder {
	return m.recorder
}

// Send mocks base method.
func (m *MockEnrollment_TPMChallengeClient) Send(req *AgentChallenge) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Send", req)
	ret0, _ := ret[0].(error)
	return ret0
}

// Send indicates an expected call of Send.
func (mr *MockEnrollment_TPMChallengeClientMockRecorder) Send(req any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Send", reflect.TypeOf((*MockEnrollment_TPMChallengeClient)(nil).Send), req)
}

// Recv mocks base method.
func (m *MockEnrollment_TPMChallengeClient) Recv() (*ServerChallenge, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Recv")
	ret0, _ := ret[0].(*ServerChallenge)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Recv indicates an expected call of Recv.
func (mr *MockEnrollment_TPMChallengeClientMockRecorder) Recv() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Recv", reflect.TypeOf((*MockEnrollment_TPMChallengeClient)(nil).Recv))
}

// CloseSend mocks base method.
func (m *MockEnrollment_TPMChallengeClient) CloseSend() error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CloseSend")
	ret0, _ := ret[0].(error)
	return ret0
}

// CloseSend indicates an expected call of CloseSend.
func (mr *MockEnrollment_TPMChallengeClientMockRecorder) CloseSend() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CloseSend", reflect.TypeOf((*MockEnrollment_TPMChallengeClient)(nil).CloseSend))
}

// Header mocks base method.
func (m *MockEnrollment_TPMChallengeClient) Header() (metadata.MD, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Header")
	ret0, _ := ret[0].(metadata.MD)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Header indicates an expected call of Header.
func (mr *MockEnrollment_TPMChallengeClientMockRecorder) Header() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Header", reflect.TypeOf((*MockEnrollment_TPMChallengeClient)(nil).Header))
}

// Trailer mocks base method.
func (m *MockEnrollment_TPMChallengeClient) Trailer() metadata.MD {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Trailer")
	ret0, _ := ret[0].(metadata.MD)
	return ret0
}

// Trailer indicates an expected call of Trailer.
func (mr *MockEnrollment_TPMChallengeClientMockRecorder) Trailer() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Trailer", reflect.TypeOf((*MockEnrollment_TPMChallengeClient)(nil).Trailer))
}

// Context mocks base method.
func (m *MockEnrollment_TPMChallengeClient) Context() context.Context {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Context")
	ret0, _ := ret[0].(context.Context)
	return ret0
}

// Context indicates an expected call of Context.
func (mr *MockEnrollment_TPMChallengeClientMockRecorder) Context() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Context", reflect.TypeOf((*MockEnrollment_TPMChallengeClient)(nil).Context))
}

// SendMsg mocks base method.
func (m *MockEnrollment_TPMChallengeClient) SendMsg(msg any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SendMsg", msg)
	ret0, _ := ret[0].(error)
	return ret0
}

// SendMsg indicates an expected call of SendMsg.
func (mr *MockEnrollment_TPMChallengeClientMockRecorder) SendMsg(msg any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SendMsg", reflect.TypeOf((*MockEnrollment_TPMChallengeClient)(nil).SendMsg), msg)
}

// RecvMsg mocks base method.
func (m *MockEnrollment_TPMChallengeClient) RecvMsg(msg any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RecvMsg", msg)
	ret0, _ := ret[0].(error)
	return ret0
}

// RecvMsg indicates an expected call of RecvMsg.
func (mr *MockEnrollment_TPMChallengeClientMockRecorder) RecvMsg(msg any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RecvMsg", reflect.TypeOf((*MockEnrollment_TPMChallengeClient)(nil).RecvMsg), msg)
}
