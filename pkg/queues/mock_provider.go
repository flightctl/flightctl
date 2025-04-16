// Code generated by MockGen. DO NOT EDIT.
// Source: provider.go
//
// Generated by this command:
//
//	mockgen -source=provider.go -destination=mock_provider.go -package=queues
//

// Package queues is a generated GoMock package.
package queues

import (
	context "context"
	reflect "reflect"

	gomock "go.uber.org/mock/gomock"
)

// MockProvider is a mock of Provider interface.
type MockProvider struct {
	ctrl     *gomock.Controller
	recorder *MockProviderMockRecorder
}

// MockProviderMockRecorder is the mock recorder for MockProvider.
type MockProviderMockRecorder struct {
	mock *MockProvider
}

// NewMockProvider creates a new mock instance.
func NewMockProvider(ctrl *gomock.Controller) *MockProvider {
	mock := &MockProvider{ctrl: ctrl}
	mock.recorder = &MockProviderMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockProvider) EXPECT() *MockProviderMockRecorder {
	return m.recorder
}

// NewConsumer mocks base method.
func (m *MockProvider) NewConsumer(queueName string) (Consumer, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "NewConsumer", queueName)
	ret0, _ := ret[0].(Consumer)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// NewConsumer indicates an expected call of NewConsumer.
func (mr *MockProviderMockRecorder) NewConsumer(queueName any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "NewConsumer", reflect.TypeOf((*MockProvider)(nil).NewConsumer), queueName)
}

// NewPublisher mocks base method.
func (m *MockProvider) NewPublisher(queueName string) (Publisher, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "NewPublisher", queueName)
	ret0, _ := ret[0].(Publisher)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// NewPublisher indicates an expected call of NewPublisher.
func (mr *MockProviderMockRecorder) NewPublisher(queueName any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "NewPublisher", reflect.TypeOf((*MockProvider)(nil).NewPublisher), queueName)
}

// Stop mocks base method.
func (m *MockProvider) Stop() {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "Stop")
}

// Stop indicates an expected call of Stop.
func (mr *MockProviderMockRecorder) Stop() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Stop", reflect.TypeOf((*MockProvider)(nil).Stop))
}

// Wait mocks base method.
func (m *MockProvider) Wait() {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "Wait")
}

// Wait indicates an expected call of Wait.
func (mr *MockProviderMockRecorder) Wait() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Wait", reflect.TypeOf((*MockProvider)(nil).Wait))
}

// MockConsumer is a mock of Consumer interface.
type MockConsumer struct {
	ctrl     *gomock.Controller
	recorder *MockConsumerMockRecorder
}

// MockConsumerMockRecorder is the mock recorder for MockConsumer.
type MockConsumerMockRecorder struct {
	mock *MockConsumer
}

// NewMockConsumer creates a new mock instance.
func NewMockConsumer(ctrl *gomock.Controller) *MockConsumer {
	mock := &MockConsumer{ctrl: ctrl}
	mock.recorder = &MockConsumerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockConsumer) EXPECT() *MockConsumerMockRecorder {
	return m.recorder
}

// Close mocks base method.
func (m *MockConsumer) Close() {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "Close")
}

// Close indicates an expected call of Close.
func (mr *MockConsumerMockRecorder) Close() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Close", reflect.TypeOf((*MockConsumer)(nil).Close))
}

// Consume mocks base method.
func (m *MockConsumer) Consume(ctx context.Context, handler ConsumeHandler) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Consume", ctx, handler)
	ret0, _ := ret[0].(error)
	return ret0
}

// Consume indicates an expected call of Consume.
func (mr *MockConsumerMockRecorder) Consume(ctx, handler any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Consume", reflect.TypeOf((*MockConsumer)(nil).Consume), ctx, handler)
}

// MockPublisher is a mock of Publisher interface.
type MockPublisher struct {
	ctrl     *gomock.Controller
	recorder *MockPublisherMockRecorder
}

// MockPublisherMockRecorder is the mock recorder for MockPublisher.
type MockPublisherMockRecorder struct {
	mock *MockPublisher
}

// NewMockPublisher creates a new mock instance.
func NewMockPublisher(ctrl *gomock.Controller) *MockPublisher {
	mock := &MockPublisher{ctrl: ctrl}
	mock.recorder = &MockPublisherMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockPublisher) EXPECT() *MockPublisherMockRecorder {
	return m.recorder
}

// Close mocks base method.
func (m *MockPublisher) Close() {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "Close")
}

// Close indicates an expected call of Close.
func (mr *MockPublisherMockRecorder) Close() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Close", reflect.TypeOf((*MockPublisher)(nil).Close))
}

// Publish mocks base method.
func (m *MockPublisher) Publish(ctx context.Context, payload []byte) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Publish", ctx, payload)
	ret0, _ := ret[0].(error)
	return ret0
}

// Publish indicates an expected call of Publish.
func (mr *MockPublisherMockRecorder) Publish(ctx, payload any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Publish", reflect.TypeOf((*MockPublisher)(nil).Publish), ctx, payload)
}
