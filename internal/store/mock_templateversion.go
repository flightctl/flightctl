// Code generated by MockGen. DO NOT EDIT.
// Source: internal/store/templateversion.go
//
// Generated by this command:
//
//	mockgen -source=internal/store/templateversion.go -destination=internal/store/mock_templateversion.go -package=store
//

// Package store is a generated GoMock package.
package store

import (
	context "context"
	reflect "reflect"

	v1alpha1 "github.com/flightctl/flightctl/api/v1alpha1"
	uuid "github.com/google/uuid"
	gomock "go.uber.org/mock/gomock"
)

// MockTemplateVersion is a mock of TemplateVersion interface.
type MockTemplateVersion struct {
	ctrl     *gomock.Controller
	recorder *MockTemplateVersionMockRecorder
}

// MockTemplateVersionMockRecorder is the mock recorder for MockTemplateVersion.
type MockTemplateVersionMockRecorder struct {
	mock *MockTemplateVersion
}

// NewMockTemplateVersion creates a new mock instance.
func NewMockTemplateVersion(ctrl *gomock.Controller) *MockTemplateVersion {
	mock := &MockTemplateVersion{ctrl: ctrl}
	mock.recorder = &MockTemplateVersionMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockTemplateVersion) EXPECT() *MockTemplateVersionMockRecorder {
	return m.recorder
}

// Create mocks base method.
func (m *MockTemplateVersion) Create(ctx context.Context, orgId uuid.UUID, templateVersion *v1alpha1.TemplateVersion, callback TemplateVersionStoreCallback) (*v1alpha1.TemplateVersion, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Create", ctx, orgId, templateVersion, callback)
	ret0, _ := ret[0].(*v1alpha1.TemplateVersion)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Create indicates an expected call of Create.
func (mr *MockTemplateVersionMockRecorder) Create(ctx, orgId, templateVersion, callback any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*MockTemplateVersion)(nil).Create), ctx, orgId, templateVersion, callback)
}

// Delete mocks base method.
func (m *MockTemplateVersion) Delete(ctx context.Context, orgId uuid.UUID, fleet, name string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", ctx, orgId, fleet, name)
	ret0, _ := ret[0].(error)
	return ret0
}

// Delete indicates an expected call of Delete.
func (mr *MockTemplateVersionMockRecorder) Delete(ctx, orgId, fleet, name any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*MockTemplateVersion)(nil).Delete), ctx, orgId, fleet, name)
}

// DeleteAll mocks base method.
func (m *MockTemplateVersion) DeleteAll(ctx context.Context, orgId uuid.UUID, fleet *string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DeleteAll", ctx, orgId, fleet)
	ret0, _ := ret[0].(error)
	return ret0
}

// DeleteAll indicates an expected call of DeleteAll.
func (mr *MockTemplateVersionMockRecorder) DeleteAll(ctx, orgId, fleet any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DeleteAll", reflect.TypeOf((*MockTemplateVersion)(nil).DeleteAll), ctx, orgId, fleet)
}

// Get mocks base method.
func (m *MockTemplateVersion) Get(ctx context.Context, orgId uuid.UUID, fleet, name string) (*v1alpha1.TemplateVersion, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", ctx, orgId, fleet, name)
	ret0, _ := ret[0].(*v1alpha1.TemplateVersion)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get indicates an expected call of Get.
func (mr *MockTemplateVersionMockRecorder) Get(ctx, orgId, fleet, name any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*MockTemplateVersion)(nil).Get), ctx, orgId, fleet, name)
}

// GetNewestValid mocks base method.
func (m *MockTemplateVersion) GetNewestValid(ctx context.Context, orgId uuid.UUID, fleet string) (*v1alpha1.TemplateVersion, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetNewestValid", ctx, orgId, fleet)
	ret0, _ := ret[0].(*v1alpha1.TemplateVersion)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetNewestValid indicates an expected call of GetNewestValid.
func (mr *MockTemplateVersionMockRecorder) GetNewestValid(ctx, orgId, fleet any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetNewestValid", reflect.TypeOf((*MockTemplateVersion)(nil).GetNewestValid), ctx, orgId, fleet)
}

// InitialMigration mocks base method.
func (m *MockTemplateVersion) InitialMigration() error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "InitialMigration")
	ret0, _ := ret[0].(error)
	return ret0
}

// InitialMigration indicates an expected call of InitialMigration.
func (mr *MockTemplateVersionMockRecorder) InitialMigration() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "InitialMigration", reflect.TypeOf((*MockTemplateVersion)(nil).InitialMigration))
}

// List mocks base method.
func (m *MockTemplateVersion) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*v1alpha1.TemplateVersionList, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", ctx, orgId, listParams)
	ret0, _ := ret[0].(*v1alpha1.TemplateVersionList)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// List indicates an expected call of List.
func (mr *MockTemplateVersionMockRecorder) List(ctx, orgId, listParams any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*MockTemplateVersion)(nil).List), ctx, orgId, listParams)
}

// UpdateStatus mocks base method.
func (m *MockTemplateVersion) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *v1alpha1.TemplateVersion, valid *bool, callback TemplateVersionStoreCallback) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpdateStatus", ctx, orgId, resource, valid, callback)
	ret0, _ := ret[0].(error)
	return ret0
}

// UpdateStatus indicates an expected call of UpdateStatus.
func (mr *MockTemplateVersionMockRecorder) UpdateStatus(ctx, orgId, resource, valid, callback any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpdateStatus", reflect.TypeOf((*MockTemplateVersion)(nil).UpdateStatus), ctx, orgId, resource, valid, callback)
}
