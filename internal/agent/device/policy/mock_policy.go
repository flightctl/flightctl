// Code generated by MockGen. DO NOT EDIT.
// Source: policy.go
//
// Generated by this command:
//
//	mockgen -source=policy.go -destination=mock_policy.go -package=policy
//

// Package policy is a generated GoMock package.
package policy

import (
	context "context"
	reflect "reflect"

	v1alpha1 "github.com/flightctl/flightctl/api/v1alpha1"
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

// IsReady mocks base method.
func (m *MockManager) IsReady(ctx context.Context, policyType Type) bool {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "IsReady", ctx, policyType)
	ret0, _ := ret[0].(bool)
	return ret0
}

// IsReady indicates an expected call of IsReady.
func (mr *MockManagerMockRecorder) IsReady(ctx, policyType any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "IsReady", reflect.TypeOf((*MockManager)(nil).IsReady), ctx, policyType)
}

// Sync mocks base method.
func (m *MockManager) Sync(ctx context.Context, desired *v1alpha1.DeviceSpec) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Sync", ctx, desired)
	ret0, _ := ret[0].(error)
	return ret0
}

// Sync indicates an expected call of Sync.
func (mr *MockManagerMockRecorder) Sync(ctx, desired any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Sync", reflect.TypeOf((*MockManager)(nil).Sync), ctx, desired)
}
