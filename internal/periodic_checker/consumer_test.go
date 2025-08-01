package periodic

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type mockPeriodicTaskExecutor struct {
	mu               sync.Mutex
	executeCallCount int
	executeCallArgs  []executeCallArgs
}

type executeCallArgs struct {
	ctx context.Context
	log logrus.FieldLogger
}

func (m *mockPeriodicTaskExecutor) Execute(ctx context.Context, log logrus.FieldLogger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executeCallCount++
	m.executeCallArgs = append(m.executeCallArgs, executeCallArgs{ctx: ctx, log: log})
}

func (m *mockPeriodicTaskExecutor) GetExecuteCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.executeCallCount
}

func (m *mockPeriodicTaskExecutor) GetExecuteCallArgs() []executeCallArgs {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Return a copy to avoid races
	result := make([]executeCallArgs, len(m.executeCallArgs))
	copy(result, m.executeCallArgs)
	return result
}

type mockConsumer struct {
	consumeHandler queues.ConsumeHandler
	consumeError   error
	closeCallCount int
	mu             sync.Mutex
}

func (m *mockConsumer) Consume(ctx context.Context, handler queues.ConsumeHandler) error {
	m.mu.Lock()
	m.consumeHandler = handler
	m.mu.Unlock()
	if m.consumeError != nil {
		return m.consumeError
	}
	// Block until context is cancelled to simulate real consumer behavior
	<-ctx.Done()
	return ctx.Err()
}

func (m *mockConsumer) Close() {
	m.mu.Lock()
	m.closeCallCount++
	m.mu.Unlock()
}

type mockProvider struct {
	newConsumerError error
	consumers        []*mockConsumer
	stopCallCount    int
	waitCallCount    int
	mu               sync.Mutex
}

func (m *mockProvider) NewConsumer(queueName string) (queues.Consumer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.newConsumerError != nil {
		return nil, m.newConsumerError
	}
	// Create a new consumer each time
	consumer := &mockConsumer{}
	m.consumers = append(m.consumers, consumer)
	return consumer, nil
}

func (m *mockProvider) NewPublisher(queueName string) (queues.Publisher, error) {
	return nil, errors.New("not implemented")
}

func (m *mockProvider) Stop() {
	m.stopCallCount++
}

func (m *mockProvider) Wait() {
	m.waitCallCount++
}

// panicPeriodicTaskExecutor is a mock executor that always panics
type panicPeriodicTaskExecutor struct {
	mu               sync.Mutex
	executeCallCount int
}

func (p *panicPeriodicTaskExecutor) Execute(ctx context.Context, log logrus.FieldLogger) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.executeCallCount++
	panic("test panic")
}

func (p *panicPeriodicTaskExecutor) GetExecuteCallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.executeCallCount
}

// Test helpers

func createTestExecutors() map[PeriodicTaskType]PeriodicTaskExecutor {
	return map[PeriodicTaskType]PeriodicTaskExecutor{
		PeriodicTaskTypeRepositoryTester:       &mockPeriodicTaskExecutor{},
		PeriodicTaskTypeResourceSync:           &mockPeriodicTaskExecutor{},
		PeriodicTaskTypeDeviceDisconnected:     &mockPeriodicTaskExecutor{},
		PeriodicTaskTypeRolloutDeviceSelection: &mockPeriodicTaskExecutor{},
		PeriodicTaskTypeDisruptionBudget:       &mockPeriodicTaskExecutor{},
		PeriodicTaskTypeEventCleanup:           &mockPeriodicTaskExecutor{},
	}
}

func createTestLogger() logrus.FieldLogger {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel) // Reduce noise in tests
	return logger
}

func createTaskReference(taskType PeriodicTaskType) PeriodicTaskReference {
	return PeriodicTaskReference{
		Type:  taskType,
		OrgID: uuid.New(),
	}
}

func TestConsumeTasks_SuccessfulTaskConsumption(t *testing.T) {
	tests := []struct {
		name     string
		taskType PeriodicTaskType
	}{
		{"RepositoryTester", PeriodicTaskTypeRepositoryTester},
		{"ResourceSync", PeriodicTaskTypeResourceSync},
		{"DeviceDisconnected", PeriodicTaskTypeDeviceDisconnected},
		{"RolloutDeviceSelection", PeriodicTaskTypeRolloutDeviceSelection},
		{"DisruptionBudget", PeriodicTaskTypeDisruptionBudget},
		{"EventCleanup", PeriodicTaskTypeEventCleanup},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executors := createTestExecutors()
			mockExecutor := executors[tt.taskType].(*mockPeriodicTaskExecutor)
			handler := consumeTasks(executors)
			log := createTestLogger()
			ctx := context.Background()

			taskRef := createTaskReference(tt.taskType)
			payload, err := json.Marshal(taskRef)
			require.NoError(t, err)

			err = handler(ctx, payload, log)

			require.NoError(t, err)
			require.Equal(t, 1, mockExecutor.GetExecuteCallCount(), "executor should be called once")
			executeCallArgs := mockExecutor.GetExecuteCallArgs()
			require.Len(t, executeCallArgs, 1)

			// Verify context contains organization ID
			executedCtx := executeCallArgs[0].ctx
			orgID, ok := util.GetOrgIdFromContext(executedCtx)
			require.True(t, ok, "organization ID should be present in context")
			require.Equal(t, taskRef.OrgID, orgID, "organization ID should be added to context")

			// Verify logger is passed
			require.NotNil(t, executeCallArgs[0].log)
		})
	}
}

func TestConsumeTasks_JSONUnmarshalingError(t *testing.T) {
	executors := createTestExecutors()
	handler := consumeTasks(executors)
	log := createTestLogger()
	ctx := context.Background()

	// Invalid JSON payload
	invalidPayload := []byte(`{"invalid": json}`)

	err := handler(ctx, invalidPayload, log)

	require.Error(t, err, "should return error for invalid JSON")

	// Verify no executors were called
	for _, executor := range executors {
		mockExecutor := executor.(*mockPeriodicTaskExecutor)
		require.Equal(t, 0, mockExecutor.GetExecuteCallCount(), "no executor should be called")
	}
}

func TestConsumeTasks_UnknownTaskType(t *testing.T) {
	executors := createTestExecutors()
	handler := consumeTasks(executors)
	log := createTestLogger()
	ctx := context.Background()

	// Valid JSON but unknown task type
	taskRef := PeriodicTaskReference{
		Type:  "unknown-task-type",
		OrgID: uuid.New(),
	}
	payload, err := json.Marshal(taskRef)
	require.NoError(t, err)

	err = handler(ctx, payload, log)

	require.NoError(t, err, "should return nil for unknown task type")

	// Verify no executors were called
	for _, executor := range executors {
		mockExecutor := executor.(*mockPeriodicTaskExecutor)
		require.Equal(t, 0, mockExecutor.GetExecuteCallCount(), "no executor should be called")
	}
}

func TestConsumeTasks_MultipleTaskTypes(t *testing.T) {
	executors := createTestExecutors()
	handler := consumeTasks(executors)
	log := createTestLogger()
	ctx := context.Background()

	testCases := []PeriodicTaskType{
		PeriodicTaskTypeRepositoryTester,
		PeriodicTaskTypeResourceSync,
		PeriodicTaskTypeEventCleanup,
	}

	// Execute each task type
	for _, taskType := range testCases {
		taskRef := createTaskReference(taskType)
		payload, err := json.Marshal(taskRef)
		require.NoError(t, err)

		err = handler(ctx, payload, log)
		require.NoError(t, err)
	}

	// Verify each corresponding executor was called exactly once
	for _, taskType := range testCases {
		mockExecutor := executors[taskType].(*mockPeriodicTaskExecutor)
		require.Equal(t, 1, mockExecutor.GetExecuteCallCount(),
			"executor for %s should be called once", taskType)
	}

	// Verify other executors were not called
	allTaskTypes := []PeriodicTaskType{
		PeriodicTaskTypeRepositoryTester,
		PeriodicTaskTypeResourceSync,
		PeriodicTaskTypeDeviceDisconnected,
		PeriodicTaskTypeRolloutDeviceSelection,
		PeriodicTaskTypeDisruptionBudget,
		PeriodicTaskTypeEventCleanup,
	}

	for _, taskType := range allTaskTypes {
		mockExecutor := executors[taskType].(*mockPeriodicTaskExecutor)
		found := false
		for _, testType := range testCases {
			if testType == taskType {
				found = true
				break
			}
		}
		if !found {
			require.Equal(t, 0, mockExecutor.GetExecuteCallCount(),
				"executor for %s should not be called", taskType)
		}
	}
}

func TestPeriodicTaskConsumer_Start_Success(t *testing.T) {
	mockProvider := &mockProvider{}
	executors := createTestExecutors()
	log := createTestLogger()

	consumer := NewPeriodicTaskConsumer(PeriodicTaskConsumerConfig{
		QueuesProvider: mockProvider,
		Log:            log,
		Executors:      executors,
	})

	ctx := context.Background()
	err := consumer.Start(ctx)

	require.NoError(t, err)

	// Wait for all consumers to start
	require.Eventually(t, func() bool {
		mockProvider.mu.Lock()
		defer mockProvider.mu.Unlock()
		return len(mockProvider.consumers) == DefaultConsumerCount
	}, 1*time.Second, 10*time.Millisecond)

	require.Len(t, mockProvider.consumers, DefaultConsumerCount, "should create default number of consumers")
	for i, mockConsumer := range mockProvider.consumers {
		mockConsumer.mu.Lock()
		handler := mockConsumer.consumeHandler
		mockConsumer.mu.Unlock()
		require.NotNil(t, handler, "consume handler should be set for consumer %d", i)
	}

	// Clean up
	consumer.Stop()
}

func TestPeriodicTaskConsumer_Start_ConsumerCreationError(t *testing.T) {
	expectedError := errors.New("failed to create consumer")
	mockProvider := &mockProvider{newConsumerError: expectedError}
	executors := createTestExecutors()
	log := createTestLogger()

	consumer := NewPeriodicTaskConsumer(PeriodicTaskConsumerConfig{
		QueuesProvider: mockProvider,
		Log:            log,
		Executors:      executors,
	})

	ctx := context.Background()
	err := consumer.Start(ctx)

	require.Error(t, err)
	require.Equal(t, expectedError, err)
}

func TestPeriodicTaskConsumer_MultipleConsumers(t *testing.T) {
	log := createTestLogger()
	executors := createTestExecutors()

	testProvider := testutil.NewTestProvider(log)
	defer testProvider.Stop()

	consumer := NewPeriodicTaskConsumer(PeriodicTaskConsumerConfig{
		QueuesProvider: testProvider,
		Log:            log,
		Executors:      executors,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start consumer
	err := consumer.Start(ctx)
	require.NoError(t, err)
	defer consumer.Stop()

	// Publish multiple test messages
	publisher, err := testProvider.NewPublisher(consts.PeriodicTaskQueue)
	require.NoError(t, err)

	numMessages := 20
	for i := 0; i < numMessages; i++ {
		taskRef := createTaskReference(PeriodicTaskTypeRepositoryTester)
		payload, err := json.Marshal(taskRef)
		require.NoError(t, err)

		err = publisher.Publish(ctx, payload)
		require.NoError(t, err)
	}

	// Wait for all messages to be processed
	mockExecutor := executors[PeriodicTaskTypeRepositoryTester].(*mockPeriodicTaskExecutor)
	require.Eventually(t, func() bool {
		return mockExecutor.GetExecuteCallCount() == numMessages
	}, 10*time.Second, 50*time.Millisecond, "all messages should be processed")

	// Verify exact number of messages processed
	require.Equal(t, numMessages, mockExecutor.GetExecuteCallCount())
}

func TestPeriodicTaskConsumer_Stop(t *testing.T) {
	mockProvider := &mockProvider{}
	executors := createTestExecutors()
	log := createTestLogger()

	consumer := NewPeriodicTaskConsumer(PeriodicTaskConsumerConfig{
		QueuesProvider: mockProvider,
		Log:            log,
		Executors:      executors,
	})

	ctx := context.Background()
	err := consumer.Start(ctx)
	require.NoError(t, err)

	// Verify consumers were created
	require.Len(t, mockProvider.consumers, 5)

	// Stop the consumer
	consumer.Stop()

	// Verify all consumers were closed
	for i, mockConsumer := range mockProvider.consumers {
		mockConsumer.mu.Lock()
		closeCount := mockConsumer.closeCallCount
		mockConsumer.mu.Unlock()
		require.Equal(t, 1, closeCount, "consumer %d should be closed once", i)
	}
}

func TestConsumeTasks_PanicRecovery(t *testing.T) {
	panicExecutor := &panicPeriodicTaskExecutor{}
	normalExecutor := &mockPeriodicTaskExecutor{}

	executors := map[PeriodicTaskType]PeriodicTaskExecutor{
		PeriodicTaskTypeRepositoryTester: panicExecutor,
		PeriodicTaskTypeResourceSync:     normalExecutor,
	}

	handler := consumeTasks(executors)
	log := createTestLogger()
	ctx := context.Background()

	// Execute a task that panics
	panicTaskRef := PeriodicTaskReference{
		Type:  PeriodicTaskTypeRepositoryTester,
		OrgID: uuid.New(),
	}
	panicPayload, err := json.Marshal(panicTaskRef)
	require.NoError(t, err)

	// Handler should not panic - the panic should be recovered
	err = handler(ctx, panicPayload, log)
	require.NoError(t, err)

	require.Equal(t, 1, panicExecutor.GetExecuteCallCount(), "panicking executor should have been called")

	// Verify the handler still works for a healthy task after a panic
	normalTaskRef := PeriodicTaskReference{
		Type:  PeriodicTaskTypeResourceSync,
		OrgID: uuid.New(),
	}
	normalPayload, err := json.Marshal(normalTaskRef)
	require.NoError(t, err)

	err = handler(ctx, normalPayload, log)
	require.NoError(t, err)

	require.Equal(t, 1, normalExecutor.GetExecuteCallCount(), "normal executor should have been called")
}
