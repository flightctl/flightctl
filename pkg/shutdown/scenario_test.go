package shutdown

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScenario_PriorityOrdering tests that components shut down in correct priority order
func TestScenario_PriorityOrdering(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := NewShutdownManager(logger)

	var shutdownOrder []string
	var mu sync.Mutex

	// Register components in different order than shutdown priority
	components := []struct {
		name     string
		priority int
	}{
		{"database", PriorityLow},
		{"http-server", PriorityHighest},
		{"background-tasks", PriorityNormal},
		{"cache", PriorityLow},
		{"grpc-server", PriorityHigh},
		{"completion", PriorityLast},
	}

	for _, comp := range components {
		name := comp.name // capture for closure
		shutdownManager.Register(name, comp.priority, TimeoutStandard, func(ctx context.Context) error {
			mu.Lock()
			shutdownOrder = append(shutdownOrder, name)
			mu.Unlock()
			return nil
		})
	}

	ctx := context.Background()
	err := shutdownManager.Shutdown(ctx)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	// Verify correct priority order: Highest -> High -> Normal -> Low -> Last
	expectedOrder := []string{"http-server", "grpc-server", "background-tasks", "database", "cache", "completion"}
	assert.Equal(t, expectedOrder, shutdownOrder, "Components should shut down in priority order")
}

// TestScenario_ComponentTimeouts tests that component timeouts are respected
func TestScenario_ComponentTimeouts(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := NewShutdownManager(logger)

	var timeoutOccurred int32
	var completedNormally int32

	// Component that completes within timeout
	shutdownManager.Register("fast-component", PriorityHigh, 200*time.Millisecond, func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond) // Well within timeout
		atomic.AddInt32(&completedNormally, 1)
		return nil
	})

	// Component that exceeds timeout
	shutdownManager.Register("slow-component", PriorityHigh, 100*time.Millisecond, func(ctx context.Context) error {
		select {
		case <-time.After(300 * time.Millisecond): // Exceeds timeout
			atomic.AddInt32(&completedNormally, 1)
			return nil
		case <-ctx.Done():
			atomic.AddInt32(&timeoutOccurred, 1)
			return ctx.Err()
		}
	})

	start := time.Now()
	ctx := context.Background()
	err := shutdownManager.Shutdown(ctx)
	duration := time.Since(start)

	// Should report error due to timeout but continue
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown completed with")

	assert.Equal(t, int32(1), atomic.LoadInt32(&completedNormally), "Fast component should complete normally")
	assert.Equal(t, int32(1), atomic.LoadInt32(&timeoutOccurred), "Slow component should be timed out")

	// Should respect the slower timeout but not wait indefinitely
	assert.GreaterOrEqual(t, duration, 100*time.Millisecond, "Should wait for timeout")
	assert.Less(t, duration, 400*time.Millisecond, "Should not wait excessively long")
}

// TestScenario_TaskCompletion tests that running tasks complete gracefully
func TestScenario_TaskCompletion(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := NewShutdownManager(logger)

	var tasksCompleted int32
	var tasksStarted int32

	// Simulate a component with multiple running tasks
	shutdownManager.Register("task-manager", PriorityHigh, 2*time.Second, func(ctx context.Context) error {
		// Simulate 5 tasks that need to complete
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(taskID int) {
				defer wg.Done()
				atomic.AddInt32(&tasksStarted, 1)

				// Simulate task work
				select {
				case <-time.After(100 * time.Millisecond):
					atomic.AddInt32(&tasksCompleted, 1)
				case <-ctx.Done():
					// Task was interrupted - still count as started
					return
				}
			}(i)
		}

		// Wait for all tasks to complete or context cancellation
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	start := time.Now()
	ctx := context.Background()
	err := shutdownManager.Shutdown(ctx)
	duration := time.Since(start)

	require.NoError(t, err)

	assert.Equal(t, int32(5), atomic.LoadInt32(&tasksStarted), "All tasks should start")
	assert.Equal(t, int32(5), atomic.LoadInt32(&tasksCompleted), "All tasks should complete gracefully")
	assert.GreaterOrEqual(t, duration, 100*time.Millisecond, "Should wait for tasks to complete")
	assert.Less(t, duration, 500*time.Millisecond, "Should complete efficiently")
}

// TestScenario_ErrorHandling tests error handling during shutdown
func TestScenario_ErrorHandling(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := NewShutdownManager(logger)

	var successCount int32
	var errorCount int32

	// Component that succeeds
	shutdownManager.Register("success-component", PriorityHigh, TimeoutStandard, func(ctx context.Context) error {
		atomic.AddInt32(&successCount, 1)
		return nil
	})

	// Component that fails
	shutdownManager.Register("error-component", PriorityHigh, TimeoutStandard, func(ctx context.Context) error {
		atomic.AddInt32(&errorCount, 1)
		return fmt.Errorf("simulated shutdown error")
	})

	// Component that panics
	shutdownManager.Register("panic-component", PriorityHigh, TimeoutStandard, func(ctx context.Context) error {
		panic("simulated panic during shutdown")
	})

	// Component that succeeds after errors
	shutdownManager.Register("final-component", PriorityLow, TimeoutStandard, func(ctx context.Context) error {
		atomic.AddInt32(&successCount, 1)
		return nil
	})

	ctx := context.Background()
	err := shutdownManager.Shutdown(ctx)

	// Should complete with errors reported
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown completed with")

	assert.Equal(t, int32(2), atomic.LoadInt32(&successCount), "Success components should complete")
	assert.Equal(t, int32(1), atomic.LoadInt32(&errorCount), "Error component should be called")
}

// TestScenario_GracefulShutdownFlow tests a realistic shutdown flow
func TestScenario_GracefulShutdownFlow(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := NewShutdownManager(logger)

	var shutdownPhases []string
	var shutdownTimes []time.Time
	var mu sync.Mutex

	// Simulate a realistic shutdown sequence
	shutdownManager.Register("load-balancer", PriorityHighest, TimeoutQuick, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "load-balancer-stop")
		shutdownTimes = append(shutdownTimes, time.Now())
		mu.Unlock()
		time.Sleep(50 * time.Millisecond) // Stop accepting new connections
		return nil
	})

	shutdownManager.Register("http-server", PriorityHigh, TimeoutStandard, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "http-server-stop")
		shutdownTimes = append(shutdownTimes, time.Now())
		mu.Unlock()
		time.Sleep(100 * time.Millisecond) // Finish processing existing requests
		return nil
	})

	shutdownManager.Register("background-workers", PriorityNormal, TimeoutStandard, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "workers-stop")
		shutdownTimes = append(shutdownTimes, time.Now())
		mu.Unlock()
		time.Sleep(150 * time.Millisecond) // Complete background tasks
		return nil
	})

	shutdownManager.Register("database-pool", PriorityLow, TimeoutDatabase, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "database-close")
		shutdownTimes = append(shutdownTimes, time.Now())
		mu.Unlock()
		time.Sleep(75 * time.Millisecond) // Close database connections
		return nil
	})

	shutdownManager.Register("cleanup", PriorityLast, TimeoutCompletion, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "cleanup-complete")
		shutdownTimes = append(shutdownTimes, time.Now())
		mu.Unlock()
		return nil
	})

	start := time.Now()
	ctx := context.Background()
	err := shutdownManager.Shutdown(ctx)
	totalDuration := time.Since(start)

	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	// Verify shutdown phases occur in correct order
	expectedPhases := []string{
		"load-balancer-stop",
		"http-server-stop",
		"workers-stop",
		"database-close",
		"cleanup-complete",
	}
	assert.Equal(t, expectedPhases, shutdownPhases, "Shutdown should follow correct phase order")

	// Verify timing - each phase should occur after the previous
	require.Len(t, shutdownTimes, 5)
	for i := 1; i < len(shutdownTimes); i++ {
		assert.True(t, shutdownTimes[i].After(shutdownTimes[i-1]) || shutdownTimes[i].Equal(shutdownTimes[i-1]),
			"Phase %d should occur after phase %d", i, i-1)
	}

	// Should take reasonable time
	assert.GreaterOrEqual(t, totalDuration, 300*time.Millisecond, "Should wait for components")
	assert.Less(t, totalDuration, 1*time.Second, "Should complete efficiently")
}

// TestScenario_ConcurrentShutdown tests concurrent shutdown requests
func TestScenario_ConcurrentShutdown(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := NewShutdownManager(logger)

	var executionCount int32

	shutdownManager.Register("test-component", PriorityNormal, TimeoutStandard, func(ctx context.Context) error {
		atomic.AddInt32(&executionCount, 1)
		time.Sleep(100 * time.Millisecond)
		return nil
	})

	// Start multiple concurrent shutdown requests
	var wg sync.WaitGroup
	errors := make([]error, 3)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			errors[index] = shutdownManager.Shutdown(ctx)
		}(i)
	}

	wg.Wait()

	// At least one should succeed
	successCount := 0
	for _, err := range errors {
		if err == nil {
			successCount++
		}
	}

	assert.Greater(t, successCount, 0, "At least one shutdown should succeed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&executionCount), "Component should execute exactly once")
}

// BenchmarkScenario_ShutdownPerformance benchmarks shutdown performance
func BenchmarkScenario_ShutdownPerformance(b *testing.B) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		shutdownManager := NewShutdownManager(logger)

		// Register multiple components
		for j := 0; j < 10; j++ {
			name := fmt.Sprintf("component-%d", j)
			priority := j%5 + 1 // Distribute across priorities
			shutdownManager.Register(name, priority, TimeoutQuick, func(ctx context.Context) error {
				time.Sleep(1 * time.Millisecond) // Minimal work
				return nil
			})
		}

		ctx := context.Background()
		_ = shutdownManager.Shutdown(ctx)
	}
}
