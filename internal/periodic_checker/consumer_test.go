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
}

func (m *mockConsumer) Consume(ctx context.Context, handler queues.ConsumeHandler) error {
	m.consumeHandler = handler
	return m.consumeError
}

func (m *mockConsumer) Close() {
	m.closeCallCount++
}

type mockProvider struct {
	newConsumerError error
	consumer         *mockConsumer
	stopCallCount    int
	waitCallCount    int
}

func (m *mockProvider) NewConsumer(queueName string) (queues.Consumer, error) {
	if m.newConsumerError != nil {
		return nil, m.newConsumerError
	}
	return m.consumer, nil
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
	mockConsumer := &mockConsumer{}
	mockProvider := &mockProvider{consumer: mockConsumer}
	executors := createTestExecutors()
	log := createTestLogger()

	consumer := &PeriodicTaskConsumer{
		queuesProvider: mockProvider,
		log:            log,
		executors:      executors,
	}

	ctx := context.Background()
	err := consumer.Start(ctx)

	require.NoError(t, err)
	require.NotNil(t, mockConsumer.consumeHandler, "consume handler should be set")
}

func TestPeriodicTaskConsumer_Start_ConsumerCreationError(t *testing.T) {
	expectedError := errors.New("failed to create consumer")
	mockProvider := &mockProvider{newConsumerError: expectedError}
	executors := createTestExecutors()
	log := createTestLogger()

	consumer := &PeriodicTaskConsumer{
		queuesProvider: mockProvider,
		log:            log,
		executors:      executors,
	}

	ctx := context.Background()
	err := consumer.Start(ctx)

	require.Error(t, err)
	require.Equal(t, expectedError, err)
}

func TestPeriodicTaskConsumer_Start_ConsumeError(t *testing.T) {
	expectedError := errors.New("failed to consume")
	mockConsumer := &mockConsumer{consumeError: expectedError}
	mockProvider := &mockProvider{consumer: mockConsumer}
	executors := createTestExecutors()
	log := createTestLogger()

	consumer := &PeriodicTaskConsumer{
		queuesProvider: mockProvider,
		log:            log,
		executors:      executors,
	}

	ctx := context.Background()
	err := consumer.Start(ctx)

	require.Error(t, err)
	require.Equal(t, expectedError, err)
}

func TestPeriodicTaskConsumer_FullMessageProcessing(t *testing.T) {
	log := createTestLogger()
	executors := createTestExecutors()

	testProvider := testutil.NewTestProvider(log)
	defer testProvider.Stop()

	consumer := NewPeriodicTaskConsumer(testProvider, log, executors)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start consumer in background
	go func() {
		err := consumer.Start(ctx)
		if err != nil {
			t.Logf("Consumer start error: %v", err)
		}
	}()

	// Publish a test message
	publisher, err := testProvider.NewPublisher(consts.PeriodicTaskQueue)
	require.NoError(t, err)

	taskRef := createTaskReference(PeriodicTaskTypeRepositoryTester)
	payload, err := json.Marshal(taskRef)
	require.NoError(t, err)

	err = publisher.Publish(ctx, payload)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mockExecutor := executors[PeriodicTaskTypeRepositoryTester].(*mockPeriodicTaskExecutor)
		return mockExecutor.GetExecuteCallCount() > 0
	}, 5*time.Second, 10*time.Millisecond, "executor should be called within timeout")

	// Verify
	mockExecutor := executors[PeriodicTaskTypeRepositoryTester].(*mockPeriodicTaskExecutor)
	require.Equal(t, 1, mockExecutor.GetExecuteCallCount())

	// Verify context contains organization ID
	executeCallArgs := mockExecutor.GetExecuteCallArgs()
	require.Len(t, executeCallArgs, 1)
	executedCtx := executeCallArgs[0].ctx
	orgID, ok := util.GetOrgIdFromContext(executedCtx)
	require.True(t, ok, "organization ID should be present in context")
	require.Equal(t, taskRef.OrgID, orgID)
}
