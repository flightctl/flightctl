// Code generated by MockGen. DO NOT EDIT.
// Source: internal/agent/device/resource/resource.go
//
// Generated by this command:
//
//	mockgen -source=internal/agent/device/resource/resource.go -destination=internal/agent/device/resource/mock_resource.go -package=resource
//

// Package resource is a generated GoMock package.
package resource

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

// Alerts mocks base method.
func (m *MockManager) Alerts() *Alerts {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Alerts")
	ret0, _ := ret[0].(*Alerts)
	return ret0
}

// Alerts indicates an expected call of Alerts.
func (mr *MockManagerMockRecorder) Alerts() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Alerts", reflect.TypeOf((*MockManager)(nil).Alerts))
}

// ClearAll mocks base method.
func (m *MockManager) ClearAll() error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ClearAll")
	ret0, _ := ret[0].(error)
	return ret0
}

// ClearAll indicates an expected call of ClearAll.
func (mr *MockManagerMockRecorder) ClearAll() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ClearAll", reflect.TypeOf((*MockManager)(nil).ClearAll))
}

// Run mocks base method.
func (m *MockManager) Run(ctx context.Context) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "Run", ctx)
}

// Run indicates an expected call of Run.
func (mr *MockManagerMockRecorder) Run(ctx any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Run", reflect.TypeOf((*MockManager)(nil).Run), ctx)
}

// Update mocks base method.
func (m *MockManager) Update(monitor *v1alpha1.ResourceMonitor) (bool, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", monitor)
	ret0, _ := ret[0].(bool)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Update indicates an expected call of Update.
func (mr *MockManagerMockRecorder) Update(monitor any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*MockManager)(nil).Update), monitor)
}

// MockMonitor is a mock of Monitor interface.
type MockMonitor[T any] struct {
	ctrl     *gomock.Controller
	recorder *MockMonitorMockRecorder[T]
}

// MockMonitorMockRecorder is the mock recorder for MockMonitor.
type MockMonitorMockRecorder[T any] struct {
	mock *MockMonitor[T]
}

// NewMockMonitor creates a new mock instance.
func NewMockMonitor[T any](ctrl *gomock.Controller) *MockMonitor[T] {
	mock := &MockMonitor[T]{ctrl: ctrl}
	mock.recorder = &MockMonitorMockRecorder[T]{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockMonitor[T]) EXPECT() *MockMonitorMockRecorder[T] {
	return m.recorder
}

// Alerts mocks base method.
func (m *MockMonitor[T]) Alerts() []v1alpha1.ResourceAlertRule {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Alerts")
	ret0, _ := ret[0].([]v1alpha1.ResourceAlertRule)
	return ret0
}

// Alerts indicates an expected call of Alerts.
func (mr *MockMonitorMockRecorder[T]) Alerts() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Alerts", reflect.TypeOf((*MockMonitor[T])(nil).Alerts))
}

// CollectUsage mocks base method.
func (m *MockMonitor[T]) CollectUsage(ctx context.Context, usage *T) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CollectUsage", ctx, usage)
	ret0, _ := ret[0].(error)
	return ret0
}

// CollectUsage indicates an expected call of CollectUsage.
func (mr *MockMonitorMockRecorder[T]) CollectUsage(ctx, usage any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CollectUsage", reflect.TypeOf((*MockMonitor[T])(nil).CollectUsage), ctx, usage)
}

// Run mocks base method.
func (m *MockMonitor[T]) Run(ctx context.Context) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "Run", ctx)
}

// Run indicates an expected call of Run.
func (mr *MockMonitorMockRecorder[T]) Run(ctx any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Run", reflect.TypeOf((*MockMonitor[T])(nil).Run), ctx)
}

// Update mocks base method.
func (m *MockMonitor[T]) Update(monitor *v1alpha1.ResourceMonitor) (bool, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", monitor)
	ret0, _ := ret[0].(bool)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Update indicates an expected call of Update.
func (mr *MockMonitorMockRecorder[T]) Update(monitor any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*MockMonitor[T])(nil).Update), monitor)
}
