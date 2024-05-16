// Code generated by MockGen. DO NOT EDIT.
// Source: internal/bootimage/manager.go
//
// Generated by this command:
//
//	mockgen -source=internal/bootimage/manager.go -destination=internal/bootimage/mock_manager.go -package=bootimage
//

// Package bootimage is a generated GoMock package.
package bootimage

import (
	context "context"
	reflect "reflect"

	gomock "go.uber.org/mock/gomock"
)

// MockManager is a mock of Manager interface.
type MockManager struct {
	ctrl     *gomock.Controller
	recorder *MockManagerMockRecorder
}

// MockManagerMockRecorder is the mock recorder for MockManager.
type MockManagerMockRecorder struct {
	mock *MockManager
}

// NewMockManager creates a new mock instance.
func NewMockManager(ctrl *gomock.Controller) *MockManager {
	mock := &MockManager{ctrl: ctrl}
	mock.recorder = &MockManagerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockManager) EXPECT() *MockManagerMockRecorder {
	return m.recorder
}

// Apply mocks base method.
func (m *MockManager) Apply(arg0 context.Context) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Apply", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Apply indicates an expected call of Apply.
func (mr *MockManagerMockRecorder) Apply(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Apply", reflect.TypeOf((*MockManager)(nil).Apply), arg0)
}

// IsDisabled mocks base method.
func (m *MockManager) IsDisabled() bool {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "IsDisabled")
	ret0, _ := ret[0].(bool)
	return ret0
}

// IsDisabled indicates an expected call of IsDisabled.
func (mr *MockManagerMockRecorder) IsDisabled() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "IsDisabled", reflect.TypeOf((*MockManager)(nil).IsDisabled))
}

// RemoveRollback mocks base method.
func (m *MockManager) RemoveRollback(arg0 context.Context) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveRollback", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// RemoveRollback indicates an expected call of RemoveRollback.
func (mr *MockManagerMockRecorder) RemoveRollback(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveRollback", reflect.TypeOf((*MockManager)(nil).RemoveRollback), arg0)
}

// RemoveStaged mocks base method.
func (m *MockManager) RemoveStaged(arg0 context.Context) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveStaged", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// RemoveStaged indicates an expected call of RemoveStaged.
func (mr *MockManagerMockRecorder) RemoveStaged(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveStaged", reflect.TypeOf((*MockManager)(nil).RemoveStaged), arg0)
}

// Status mocks base method.
func (m *MockManager) Status(arg0 context.Context) (*HostStatus, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Status", arg0)
	ret0, _ := ret[0].(*HostStatus)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Status indicates an expected call of Status.
func (mr *MockManagerMockRecorder) Status(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Status", reflect.TypeOf((*MockManager)(nil).Status), arg0)
}

// Switch mocks base method.
func (m *MockManager) Switch(arg0 context.Context, arg1 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Switch", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Switch indicates an expected call of Switch.
func (mr *MockManagerMockRecorder) Switch(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Switch", reflect.TypeOf((*MockManager)(nil).Switch), arg0, arg1)
}
