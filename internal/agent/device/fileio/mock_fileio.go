// Code generated by MockGen. DO NOT EDIT.
// Source: internal/agent/device/fileio/fileio.go
//
// Generated by this command:
//
//	mockgen -source=internal/agent/device/fileio/fileio.go -destination=internal/agent/device/fileio/mock_fileio.go -package=fileio
//

// Package fileio is a generated GoMock package.
package fileio

import (
	fs "io/fs"
	reflect "reflect"

	types "github.com/coreos/ignition/v2/config/v3_4/types"
	gomock "go.uber.org/mock/gomock"
)

// MockManagedFile is a mock of ManagedFile interface.
type MockManagedFile struct {
	ctrl     *gomock.Controller
	recorder *MockManagedFileMockRecorder
}

// MockManagedFileMockRecorder is the mock recorder for MockManagedFile.
type MockManagedFileMockRecorder struct {
	mock *MockManagedFile
}

// NewMockManagedFile creates a new mock instance.
func NewMockManagedFile(ctrl *gomock.Controller) *MockManagedFile {
	mock := &MockManagedFile{ctrl: ctrl}
	mock.recorder = &MockManagedFileMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockManagedFile) EXPECT() *MockManagedFileMockRecorder {
	return m.recorder
}

// Exists mocks base method.
func (m *MockManagedFile) Exists() (bool, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Exists")
	ret0, _ := ret[0].(bool)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Exists indicates an expected call of Exists.
func (mr *MockManagedFileMockRecorder) Exists() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Exists", reflect.TypeOf((*MockManagedFile)(nil).Exists))
}

// IsUpToDate mocks base method.
func (m *MockManagedFile) IsUpToDate() (bool, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "IsUpToDate")
	ret0, _ := ret[0].(bool)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// IsUpToDate indicates an expected call of IsUpToDate.
func (mr *MockManagedFileMockRecorder) IsUpToDate() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "IsUpToDate", reflect.TypeOf((*MockManagedFile)(nil).IsUpToDate))
}

// Path mocks base method.
func (m *MockManagedFile) Path() string {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Path")
	ret0, _ := ret[0].(string)
	return ret0
}

// Path indicates an expected call of Path.
func (mr *MockManagedFileMockRecorder) Path() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Path", reflect.TypeOf((*MockManagedFile)(nil).Path))
}

// Write mocks base method.
func (m *MockManagedFile) Write() error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Write")
	ret0, _ := ret[0].(error)
	return ret0
}

// Write indicates an expected call of Write.
func (mr *MockManagedFileMockRecorder) Write() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Write", reflect.TypeOf((*MockManagedFile)(nil).Write))
}

// MockWriter is a mock of Writer interface.
type MockWriter struct {
	ctrl     *gomock.Controller
	recorder *MockWriterMockRecorder
}

// MockWriterMockRecorder is the mock recorder for MockWriter.
type MockWriterMockRecorder struct {
	mock *MockWriter
}

// NewMockWriter creates a new mock instance.
func NewMockWriter(ctrl *gomock.Controller) *MockWriter {
	mock := &MockWriter{ctrl: ctrl}
	mock.recorder = &MockWriterMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockWriter) EXPECT() *MockWriterMockRecorder {
	return m.recorder
}

// CopyFile mocks base method.
func (m *MockWriter) CopyFile(src, dst string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CopyFile", src, dst)
	ret0, _ := ret[0].(error)
	return ret0
}

// CopyFile indicates an expected call of CopyFile.
func (mr *MockWriterMockRecorder) CopyFile(src, dst any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CopyFile", reflect.TypeOf((*MockWriter)(nil).CopyFile), src, dst)
}

// CreateManagedFile mocks base method.
func (m *MockWriter) CreateManagedFile(file types.File) (ManagedFile, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CreateManagedFile", file)
	ret0, _ := ret[0].(ManagedFile)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// CreateManagedFile indicates an expected call of CreateManagedFile.
func (mr *MockWriterMockRecorder) CreateManagedFile(file any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CreateManagedFile", reflect.TypeOf((*MockWriter)(nil).CreateManagedFile), file)
}

// MkdirAll mocks base method.
func (m *MockWriter) MkdirAll(path string, perm fs.FileMode) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "MkdirAll", path, perm)
	ret0, _ := ret[0].(error)
	return ret0
}

// MkdirAll indicates an expected call of MkdirAll.
func (mr *MockWriterMockRecorder) MkdirAll(path, perm any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "MkdirAll", reflect.TypeOf((*MockWriter)(nil).MkdirAll), path, perm)
}

// PathFor mocks base method.
func (m *MockWriter) PathFor(filePath string) string {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "PathFor", filePath)
	ret0, _ := ret[0].(string)
	return ret0
}

// PathFor indicates an expected call of PathFor.
func (mr *MockWriterMockRecorder) PathFor(filePath any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "PathFor", reflect.TypeOf((*MockWriter)(nil).PathFor), filePath)
}

// RemoveAll mocks base method.
func (m *MockWriter) RemoveAll(path string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveAll", path)
	ret0, _ := ret[0].(error)
	return ret0
}

// RemoveAll indicates an expected call of RemoveAll.
func (mr *MockWriterMockRecorder) RemoveAll(path any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveAll", reflect.TypeOf((*MockWriter)(nil).RemoveAll), path)
}

// RemoveFile mocks base method.
func (m *MockWriter) RemoveFile(file string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveFile", file)
	ret0, _ := ret[0].(error)
	return ret0
}

// RemoveFile indicates an expected call of RemoveFile.
func (mr *MockWriterMockRecorder) RemoveFile(file any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveFile", reflect.TypeOf((*MockWriter)(nil).RemoveFile), file)
}

// SetRootdir mocks base method.
func (m *MockWriter) SetRootdir(path string) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "SetRootdir", path)
}

// SetRootdir indicates an expected call of SetRootdir.
func (mr *MockWriterMockRecorder) SetRootdir(path any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetRootdir", reflect.TypeOf((*MockWriter)(nil).SetRootdir), path)
}

// WriteFile mocks base method.
func (m *MockWriter) WriteFile(name string, data []byte, perm fs.FileMode, opts ...FileOption) error {
	m.ctrl.T.Helper()
	varargs := []any{name, data, perm}
	for _, a := range opts {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "WriteFile", varargs...)
	ret0, _ := ret[0].(error)
	return ret0
}

// WriteFile indicates an expected call of WriteFile.
func (mr *MockWriterMockRecorder) WriteFile(name, data, perm any, opts ...any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	varargs := append([]any{name, data, perm}, opts...)
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "WriteFile", reflect.TypeOf((*MockWriter)(nil).WriteFile), varargs...)
}

// MockReader is a mock of Reader interface.
type MockReader struct {
	ctrl     *gomock.Controller
	recorder *MockReaderMockRecorder
}

// MockReaderMockRecorder is the mock recorder for MockReader.
type MockReaderMockRecorder struct {
	mock *MockReader
}

// NewMockReader creates a new mock instance.
func NewMockReader(ctrl *gomock.Controller) *MockReader {
	mock := &MockReader{ctrl: ctrl}
	mock.recorder = &MockReaderMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockReader) EXPECT() *MockReaderMockRecorder {
	return m.recorder
}

// PathExists mocks base method.
func (m *MockReader) PathExists(path string) (bool, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "PathExists", path)
	ret0, _ := ret[0].(bool)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// PathExists indicates an expected call of PathExists.
func (mr *MockReaderMockRecorder) PathExists(path any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "PathExists", reflect.TypeOf((*MockReader)(nil).PathExists), path)
}

// PathFor mocks base method.
func (m *MockReader) PathFor(filePath string) string {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "PathFor", filePath)
	ret0, _ := ret[0].(string)
	return ret0
}

// PathFor indicates an expected call of PathFor.
func (mr *MockReaderMockRecorder) PathFor(filePath any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "PathFor", reflect.TypeOf((*MockReader)(nil).PathFor), filePath)
}

// ReadDir mocks base method.
func (m *MockReader) ReadDir(dirPath string) ([]fs.DirEntry, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ReadDir", dirPath)
	ret0, _ := ret[0].([]fs.DirEntry)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// ReadDir indicates an expected call of ReadDir.
func (mr *MockReaderMockRecorder) ReadDir(dirPath any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ReadDir", reflect.TypeOf((*MockReader)(nil).ReadDir), dirPath)
}

// ReadFile mocks base method.
func (m *MockReader) ReadFile(filePath string) ([]byte, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ReadFile", filePath)
	ret0, _ := ret[0].([]byte)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// ReadFile indicates an expected call of ReadFile.
func (mr *MockReaderMockRecorder) ReadFile(filePath any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ReadFile", reflect.TypeOf((*MockReader)(nil).ReadFile), filePath)
}

// SetRootdir mocks base method.
func (m *MockReader) SetRootdir(path string) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "SetRootdir", path)
}

// SetRootdir indicates an expected call of SetRootdir.
func (mr *MockReaderMockRecorder) SetRootdir(path any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetRootdir", reflect.TypeOf((*MockReader)(nil).SetRootdir), path)
}

// MockReadWriter is a mock of ReadWriter interface.
type MockReadWriter struct {
	ctrl     *gomock.Controller
	recorder *MockReadWriterMockRecorder
}

// MockReadWriterMockRecorder is the mock recorder for MockReadWriter.
type MockReadWriterMockRecorder struct {
	mock *MockReadWriter
}

// NewMockReadWriter creates a new mock instance.
func NewMockReadWriter(ctrl *gomock.Controller) *MockReadWriter {
	mock := &MockReadWriter{ctrl: ctrl}
	mock.recorder = &MockReadWriterMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockReadWriter) EXPECT() *MockReadWriterMockRecorder {
	return m.recorder
}

// CopyFile mocks base method.
func (m *MockReadWriter) CopyFile(src, dst string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CopyFile", src, dst)
	ret0, _ := ret[0].(error)
	return ret0
}

// CopyFile indicates an expected call of CopyFile.
func (mr *MockReadWriterMockRecorder) CopyFile(src, dst any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CopyFile", reflect.TypeOf((*MockReadWriter)(nil).CopyFile), src, dst)
}

// CreateManagedFile mocks base method.
func (m *MockReadWriter) CreateManagedFile(file types.File) (ManagedFile, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CreateManagedFile", file)
	ret0, _ := ret[0].(ManagedFile)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// CreateManagedFile indicates an expected call of CreateManagedFile.
func (mr *MockReadWriterMockRecorder) CreateManagedFile(file any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CreateManagedFile", reflect.TypeOf((*MockReadWriter)(nil).CreateManagedFile), file)
}

// MkdirAll mocks base method.
func (m *MockReadWriter) MkdirAll(path string, perm fs.FileMode) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "MkdirAll", path, perm)
	ret0, _ := ret[0].(error)
	return ret0
}

// MkdirAll indicates an expected call of MkdirAll.
func (mr *MockReadWriterMockRecorder) MkdirAll(path, perm any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "MkdirAll", reflect.TypeOf((*MockReadWriter)(nil).MkdirAll), path, perm)
}

// PathExists mocks base method.
func (m *MockReadWriter) PathExists(path string) (bool, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "PathExists", path)
	ret0, _ := ret[0].(bool)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// PathExists indicates an expected call of PathExists.
func (mr *MockReadWriterMockRecorder) PathExists(path any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "PathExists", reflect.TypeOf((*MockReadWriter)(nil).PathExists), path)
}

// PathFor mocks base method.
func (m *MockReadWriter) PathFor(filePath string) string {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "PathFor", filePath)
	ret0, _ := ret[0].(string)
	return ret0
}

// PathFor indicates an expected call of PathFor.
func (mr *MockReadWriterMockRecorder) PathFor(filePath any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "PathFor", reflect.TypeOf((*MockReadWriter)(nil).PathFor), filePath)
}

// ReadDir mocks base method.
func (m *MockReadWriter) ReadDir(dirPath string) ([]fs.DirEntry, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ReadDir", dirPath)
	ret0, _ := ret[0].([]fs.DirEntry)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// ReadDir indicates an expected call of ReadDir.
func (mr *MockReadWriterMockRecorder) ReadDir(dirPath any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ReadDir", reflect.TypeOf((*MockReadWriter)(nil).ReadDir), dirPath)
}

// ReadFile mocks base method.
func (m *MockReadWriter) ReadFile(filePath string) ([]byte, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ReadFile", filePath)
	ret0, _ := ret[0].([]byte)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// ReadFile indicates an expected call of ReadFile.
func (mr *MockReadWriterMockRecorder) ReadFile(filePath any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ReadFile", reflect.TypeOf((*MockReadWriter)(nil).ReadFile), filePath)
}

// RemoveAll mocks base method.
func (m *MockReadWriter) RemoveAll(path string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveAll", path)
	ret0, _ := ret[0].(error)
	return ret0
}

// RemoveAll indicates an expected call of RemoveAll.
func (mr *MockReadWriterMockRecorder) RemoveAll(path any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveAll", reflect.TypeOf((*MockReadWriter)(nil).RemoveAll), path)
}

// RemoveFile mocks base method.
func (m *MockReadWriter) RemoveFile(file string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveFile", file)
	ret0, _ := ret[0].(error)
	return ret0
}

// RemoveFile indicates an expected call of RemoveFile.
func (mr *MockReadWriterMockRecorder) RemoveFile(file any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveFile", reflect.TypeOf((*MockReadWriter)(nil).RemoveFile), file)
}

// SetRootdir mocks base method.
func (m *MockReadWriter) SetRootdir(path string) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "SetRootdir", path)
}

// SetRootdir indicates an expected call of SetRootdir.
func (mr *MockReadWriterMockRecorder) SetRootdir(path any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SetRootdir", reflect.TypeOf((*MockReadWriter)(nil).SetRootdir), path)
}

// WriteFile mocks base method.
func (m *MockReadWriter) WriteFile(name string, data []byte, perm fs.FileMode, opts ...FileOption) error {
	m.ctrl.T.Helper()
	varargs := []any{name, data, perm}
	for _, a := range opts {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "WriteFile", varargs...)
	ret0, _ := ret[0].(error)
	return ret0
}

// WriteFile indicates an expected call of WriteFile.
func (mr *MockReadWriterMockRecorder) WriteFile(name, data, perm any, opts ...any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	varargs := append([]any{name, data, perm}, opts...)
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "WriteFile", reflect.TypeOf((*MockReadWriter)(nil).WriteFile), varargs...)
}
