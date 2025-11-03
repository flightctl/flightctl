package shutdown

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskManager_ShouldAcceptNewTasks(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)
	tm := sm.NewTaskManager("test-task")

	// Initially should accept tasks
	assert.True(t, sm.ShouldAcceptNewTasks(), "Should accept tasks when idle")
	assert.True(t, tm.CanStartTask(), "Task manager should allow starting tasks when idle")

	// Register a slow component to keep shutdown in progress longer
	sm.Register("slow-component", PriorityHigh, TimeoutStandard, func(ctx context.Context) error {
		time.Sleep(200 * time.Millisecond)
		return nil
	})

	// Start shutdown process
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		time.Sleep(10 * time.Millisecond) // Allow test to check initial state
		_ = sm.Shutdown(context.Background())
	}()

	// Wait for shutdown to begin
	time.Sleep(50 * time.Millisecond)

	// Now should reject new tasks
	assert.False(t, sm.ShouldAcceptNewTasks(), "Should reject tasks during shutdown")
	assert.False(t, tm.CanStartTask(), "Task manager should reject tasks during shutdown")

	// Wait for shutdown to complete
	<-shutdownDone
}

func TestTaskManager_PeriodicTask_StopsOnShutdown(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)
	tm := sm.NewTaskManager("periodic-task")

	// Register a component to make shutdown take longer, giving periodic task time to detect shutdown
	sm.Register("delay-component", PriorityLow, TimeoutStandard, func(ctx context.Context) error {
		time.Sleep(200 * time.Millisecond) // Give periodic task time to detect shutdown
		return nil
	})

	var executionCount int
	var currentlyExecuting bool
	var mu sync.Mutex

	taskFunc := func(ctx context.Context) error {
		mu.Lock()
		executionCount++
		currentlyExecuting = true
		currentCount := executionCount
		mu.Unlock()

		// Simulate some work - long enough that shutdown can happen during execution
		time.Sleep(100 * time.Millisecond)

		mu.Lock()
		currentlyExecuting = false
		mu.Unlock()

		logger.Infof("Task execution %d completed", currentCount)
		return nil
	}

	ctx := context.Background()
	done, err := tm.StartPeriodicTask(ctx, 80*time.Millisecond, taskFunc)
	require.NoError(t, err)

	// Let task run for a bit
	time.Sleep(150 * time.Millisecond)

	// Check some executions happened
	mu.Lock()
	initialCount := executionCount
	mu.Unlock()
	assert.Greater(t, initialCount, 0, "Task should have executed at least once")

	// Wait for a task to be executing
	for {
		mu.Lock()
		executing := currentlyExecuting
		mu.Unlock()
		if executing {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Start shutdown while task is executing
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = sm.Shutdown(context.Background())
	}()

	// Wait for task to stop
	select {
	case <-done:
		// Task stopped as expected
	case <-time.After(2 * time.Second):
		t.Fatal("Periodic task did not stop within timeout")
	}

	// Ensure shutdown completed
	<-shutdownDone

	// Verify the currently executing task completed
	mu.Lock()
	finalCount := executionCount
	stillExecuting := currentlyExecuting
	mu.Unlock()

	assert.False(t, stillExecuting, "No task should be executing after shutdown")
	assert.GreaterOrEqual(t, finalCount, initialCount, "Currently executing task should have completed")

	// Verify no new executions start after shutdown
	time.Sleep(200 * time.Millisecond) // Wait longer than the interval

	mu.Lock()
	postShutdownCount := executionCount
	mu.Unlock()

	assert.Equal(t, finalCount, postShutdownCount, "No new executions should occur after shutdown")
}

func TestTaskManager_PeriodicTask_RejectsWhenShutdownInProgress(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)
	tm := sm.NewTaskManager("test-task")

	// Start shutdown first
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		time.Sleep(10 * time.Millisecond) // Let test setup
		_ = sm.Shutdown(context.Background())
	}()

	// Wait for shutdown to start
	time.Sleep(50 * time.Millisecond)

	// Try to start periodic task - should fail
	taskFunc := func(ctx context.Context) error {
		return nil
	}

	ctx := context.Background()
	_, err := tm.StartPeriodicTask(ctx, 100*time.Millisecond, taskFunc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown in progress")

	<-shutdownDone
}

func TestTaskManager_ExecuteTask_RejectsWhenShutdownInProgress(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)
	tm := sm.NewTaskManager("test-task")

	// Start shutdown first
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		time.Sleep(10 * time.Millisecond)
		_ = sm.Shutdown(context.Background())
	}()

	// Wait for shutdown to start
	time.Sleep(50 * time.Millisecond)

	// Try to execute task - should fail
	taskFunc := func(ctx context.Context) error {
		return nil
	}

	err := tm.ExecuteTask(context.Background(), taskFunc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown in progress")

	<-shutdownDone
}

func TestTaskManager_WaitForShutdownSignal(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)
	tm := sm.NewTaskManager("test-task")

	// Register a component to make shutdown observable
	sm.Register("test-component", PriorityNormal, TimeoutStandard, func(ctx context.Context) error {
		time.Sleep(100 * time.Millisecond)
		return nil
	})

	// Start waiting for shutdown signal
	shutdownSignal := tm.WaitForShutdownSignal()

	// Initially should not be signaled
	select {
	case <-shutdownSignal:
		t.Fatal("Shutdown signal should not be triggered initially")
	case <-time.After(50 * time.Millisecond):
		// Expected - no signal yet
	}

	// Start shutdown
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = sm.Shutdown(context.Background())
	}()

	// Now should receive shutdown signal
	select {
	case <-shutdownSignal:
		// Expected
	case <-time.After(2 * time.Second):
		t.Fatal("Should have received shutdown signal")
	}

	<-shutdownDone
}

func TestGracefulTaskShutdown_StopsTasksOnShutdown(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)
	tm := sm.NewTaskManager("test-task")

	ctx := context.Background()
	gts := tm.NewGracefulTaskShutdown(ctx)

	var taskRunning, taskStopped bool
	var mu sync.Mutex

	// Start a long-running task
	err := gts.StartTask(func(ctx context.Context) error {
		mu.Lock()
		taskRunning = true
		mu.Unlock()

		// Simulate work
		select {
		case <-time.After(5 * time.Second): // Long task
			return nil
		case <-ctx.Done():
			mu.Lock()
			taskStopped = true
			mu.Unlock()
			return ctx.Err()
		}
	})
	require.NoError(t, err)

	// Verify task started
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	assert.True(t, taskRunning, "Task should be running")
	assert.False(t, taskStopped, "Task should not be stopped yet")
	mu.Unlock()

	// Start shutdown
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = sm.Shutdown(context.Background())
	}()

	// Graceful shutdown should stop tasks
	err = gts.Shutdown(2 * time.Second)
	assert.NoError(t, err, "Graceful shutdown should complete successfully")

	// Verify task was stopped
	mu.Lock()
	assert.True(t, taskStopped, "Task should have been stopped by graceful shutdown")
	mu.Unlock()

	<-shutdownDone
}

func TestGracefulTaskShutdown_TimeoutHandling(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)
	tm := sm.NewTaskManager("test-task")

	ctx := context.Background()
	gts := tm.NewGracefulTaskShutdown(ctx)

	// Start a task that ignores context cancellation
	err := gts.StartTask(func(ctx context.Context) error {
		time.Sleep(5 * time.Second) // Ignore context, sleep for long time
		return nil
	})
	require.NoError(t, err)

	// Start shutdown
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = sm.Shutdown(context.Background())
	}()

	// Graceful shutdown with short timeout should timeout
	start := time.Now()
	err = gts.Shutdown(200 * time.Millisecond)
	duration := time.Since(start)

	assert.Error(t, err, "Graceful shutdown should timeout")
	assert.Contains(t, err.Error(), "timed out")
	assert.GreaterOrEqual(t, duration, 200*time.Millisecond, "Should wait at least the timeout duration")
	assert.Less(t, duration, 500*time.Millisecond, "Should not wait much longer than timeout")

	<-shutdownDone
}

func TestTaskManager_StateTransitions(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)

	// Test initial state
	assert.Equal(t, StateIdle, sm.GetShutdownState())
	assert.False(t, sm.IsShutdownInProgress())
	assert.False(t, sm.IsShutdownCompleted())
	assert.True(t, sm.ShouldAcceptNewTasks())

	// Register a component to make shutdown take some time
	sm.Register("test-component", PriorityNormal, TimeoutStandard, func(ctx context.Context) error {
		time.Sleep(100 * time.Millisecond)
		return nil
	})

	// Start shutdown
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = sm.Shutdown(context.Background())
	}()

	// Wait for shutdown to begin
	time.Sleep(50 * time.Millisecond)

	// Should be in progress
	assert.True(t, sm.IsShutdownInProgress())
	assert.False(t, sm.IsShutdownCompleted())
	assert.False(t, sm.ShouldAcceptNewTasks())

	// Wait for shutdown to complete
	<-shutdownDone

	// Should be completed
	assert.False(t, sm.IsShutdownInProgress())
	assert.True(t, sm.IsShutdownCompleted())
	assert.False(t, sm.ShouldAcceptNewTasks()) // Still should not accept new tasks after completion
}

// Integration test demonstrating real-world usage
func TestTaskManager_IntegrationExample(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)

	// Simulate a service with multiple types of tasks
	var periodicTaskCount, oneOffTaskCount int
	var mu sync.Mutex

	// 1. Start a periodic background task
	backgroundTasks := sm.NewTaskManager("background-processor")
	periodicDone, err := backgroundTasks.StartPeriodicTask(context.Background(), 50*time.Millisecond, func(ctx context.Context) error {
		mu.Lock()
		periodicTaskCount++
		mu.Unlock()
		return nil
	})
	require.NoError(t, err)

	// 2. Set up graceful task shutdown for request handlers
	requestHandlers := sm.NewTaskManager("request-handlers")
	gts := requestHandlers.NewGracefulTaskShutdown(context.Background())

	// 3. Simulate incoming requests
	for i := 0; i < 3; i++ {
		err := gts.StartTask(func(ctx context.Context) error {
			// Simulate request processing
			select {
			case <-time.After(100 * time.Millisecond):
				mu.Lock()
				oneOffTaskCount++
				mu.Unlock()
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		require.NoError(t, err)
	}

	// Let tasks run for a bit
	time.Sleep(200 * time.Millisecond)

	// Verify tasks are running
	mu.Lock()
	assert.Greater(t, periodicTaskCount, 0, "Periodic tasks should be running")
	mu.Unlock()

	// Wait a bit longer than the periodic interval to ensure no race between count capture and shutdown
	time.Sleep(60 * time.Millisecond)

	// Register graceful shutdown for request handlers BEFORE shutdown starts
	sm.Register("request-handlers", PriorityHighest, TimeoutStandard, func(ctx context.Context) error {
		return gts.Shutdown(TimeoutStandard)
	})

	// Also add a delay component to ensure shutdown takes time for periodic task to detect
	sm.Register("delay-component", PriorityLow, TimeoutStandard, func(ctx context.Context) error {
		time.Sleep(100 * time.Millisecond)
		return nil
	})

	// Initiate shutdown
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = sm.Shutdown(context.Background())
	}()

	// Wait for periodic task to stop
	select {
	case <-periodicDone:
		// Periodic task stopped as expected
	case <-time.After(2 * time.Second):
		t.Fatal("Periodic task should have stopped")
	}

	// Capture the periodic count now that the task has stopped
	mu.Lock()
	periodicCountAfterTaskStop := periodicTaskCount
	mu.Unlock()

	// Wait for shutdown to complete
	<-shutdownDone

	// Verify final state
	mu.Lock()
	finalPeriodicCount := periodicTaskCount
	finalOneOffCount := oneOffTaskCount
	mu.Unlock()

	assert.Equal(t, periodicCountAfterTaskStop, finalPeriodicCount, "No new periodic tasks after shutdown")
	assert.Equal(t, 3, finalOneOffCount, "All one-off tasks should have completed")

	// Verify new tasks are rejected
	assert.False(t, backgroundTasks.CanStartTask(), "Should reject new background tasks")
	assert.False(t, requestHandlers.CanStartTask(), "Should reject new request handler tasks")
}
