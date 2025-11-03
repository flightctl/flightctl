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

// TestTaskCompletion_RunningTaskFinishes verifies that a task running during shutdown completes
func TestTaskCompletion_RunningTaskFinishes(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)
	tm := sm.NewTaskManager("completion-test")

	// Register a component to make shutdown observable
	sm.Register("test-component", PriorityNormal, TimeoutStandard, func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	})

	var taskStarted, taskCompleted bool
	var mu sync.Mutex

	longRunningTask := func(ctx context.Context) error {
		mu.Lock()
		taskStarted = true
		mu.Unlock()

		// Simulate work that takes longer than the shutdown trigger timing
		time.Sleep(200 * time.Millisecond)

		mu.Lock()
		taskCompleted = true
		mu.Unlock()

		return nil
	}

	// Start the periodic task with a longer interval
	done, err := tm.StartPeriodicTask(context.Background(), 500*time.Millisecond, longRunningTask)
	require.NoError(t, err)

	// Wait for the first task to start
	for {
		mu.Lock()
		started := taskStarted
		mu.Unlock()
		if started {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// At this point, the task is running. Now trigger shutdown
	shutdownStart := time.Now()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = sm.Shutdown(context.Background())
	}()

	// Wait for the periodic task to stop gracefully
	select {
	case <-done:
		// Task manager stopped
	case <-time.After(2 * time.Second):
		t.Fatal("Periodic task should have stopped within timeout")
	}

	shutdownDuration := time.Since(shutdownStart)

	// Wait for shutdown to complete
	<-shutdownDone

	// Verify the task that was running during shutdown completed
	mu.Lock()
	completed := taskCompleted
	mu.Unlock()

	assert.True(t, completed, "Task running during shutdown should have completed")
	// Allow for some timing variance - the task should take close to 200ms but account for timing precision
	assert.GreaterOrEqual(t, shutdownDuration, 180*time.Millisecond, "Shutdown should have waited for running task to complete")
	assert.LessOrEqual(t, shutdownDuration, 250*time.Millisecond, "Shutdown should not take significantly longer than task duration")
}

// TestTaskCompletion_NoNewTasksAfterShutdown verifies no new tasks start after shutdown begins
func TestTaskCompletion_NoNewTasksAfterShutdown(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)
	tm := sm.NewTaskManager("no-new-tasks-test")

	// Register a component to make shutdown observable
	sm.Register("test-component", PriorityNormal, TimeoutStandard, func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	})

	var taskCount int
	var mu sync.Mutex

	quickTask := func(ctx context.Context) error {
		mu.Lock()
		taskCount++
		count := taskCount
		mu.Unlock()

		logger.Infof("Task execution %d", count)
		time.Sleep(50 * time.Millisecond)
		return nil
	}

	// Start periodic task with short interval
	done, err := tm.StartPeriodicTask(context.Background(), 80*time.Millisecond, quickTask)
	require.NoError(t, err)

	// Let it run for a bit to get some executions
	time.Sleep(200 * time.Millisecond)

	// Check we have some executions
	mu.Lock()
	initialCount := taskCount
	mu.Unlock()
	assert.Greater(t, initialCount, 1, "Should have multiple task executions before shutdown")

	// Start shutdown
	shutdownStart := time.Now()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = sm.Shutdown(context.Background())
	}()

	// Wait for periodic task to stop
	select {
	case <-done:
		// Task stopped as expected
	case <-time.After(2 * time.Second):
		t.Fatal("Periodic task should have stopped")
	}

	shutdownDuration := time.Since(shutdownStart)
	<-shutdownDone

	// Record final count immediately after shutdown
	mu.Lock()
	finalCount := taskCount
	mu.Unlock()

	// Wait much longer than the interval to ensure no new tasks start
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	postShutdownCount := taskCount
	mu.Unlock()

	assert.Equal(t, finalCount, postShutdownCount, "No new tasks should start after shutdown")
	assert.Less(t, shutdownDuration, 2*time.Second, "Shutdown should complete in reasonable time")

	logger.Infof("Task counts: initial=%d, final=%d, post-shutdown=%d",
		initialCount, finalCount, postShutdownCount)
}

// TestTaskCompletion_MultiplePeriodicTasks verifies multiple periodic tasks behave correctly
func TestTaskCompletion_MultiplePeriodicTasks(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(logger)

	// Register a component to make shutdown observable
	sm.Register("test-component", PriorityNormal, TimeoutStandard, func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	})

	var task1Count, task2Count int
	var mu sync.Mutex

	// Create two task managers
	tm1 := sm.NewTaskManager("task-1")
	tm2 := sm.NewTaskManager("task-2")

	task1Func := func(ctx context.Context) error {
		mu.Lock()
		task1Count++
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		return nil
	}

	task2Func := func(ctx context.Context) error {
		mu.Lock()
		task2Count++
		mu.Unlock()
		time.Sleep(80 * time.Millisecond)
		return nil
	}

	// Start both periodic tasks
	done1, err := tm1.StartPeriodicTask(context.Background(), 150*time.Millisecond, task1Func)
	require.NoError(t, err)

	done2, err := tm2.StartPeriodicTask(context.Background(), 120*time.Millisecond, task2Func)
	require.NoError(t, err)

	// Let them run for a bit
	time.Sleep(300 * time.Millisecond)

	// Start shutdown
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = sm.Shutdown(context.Background())
	}()

	// Wait for both to stop
	select {
	case <-done1:
		// Task 1 stopped
	case <-time.After(2 * time.Second):
		t.Fatal("Task 1 should have stopped")
	}

	select {
	case <-done2:
		// Task 2 stopped
	case <-time.After(2 * time.Second):
		t.Fatal("Task 2 should have stopped")
	}

	<-shutdownDone

	// Verify both tasks ran and stopped
	mu.Lock()
	final1 := task1Count
	final2 := task2Count
	mu.Unlock()

	assert.Greater(t, final1, 0, "Task 1 should have executed")
	assert.Greater(t, final2, 0, "Task 2 should have executed")

	// Verify no new executions after shutdown
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	post1 := task1Count
	post2 := task2Count
	mu.Unlock()

	assert.Equal(t, final1, post1, "Task 1 should not execute after shutdown")
	assert.Equal(t, final2, post2, "Task 2 should not execute after shutdown")
}
