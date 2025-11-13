package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeriodicService_GracefulShutdown(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := shutdown.NewShutdownManager(logger)

	// Track shutdown phases and timing
	var shutdownPhases []string
	var shutdownTimes []time.Time
	var mu sync.Mutex

	// Mock periodic server with special shutdown handling
	shutdownManager.Register("periodic-server", shutdown.PriorityHigh, shutdown.TimeoutPeriodic, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "periodic-server-start")
		shutdownTimes = append(shutdownTimes, time.Now())
		mu.Unlock()

		// Simulate waiting for periodic tasks to complete
		select {
		case <-time.After(100 * time.Millisecond): // Simulate task completion time
			mu.Lock()
			shutdownPhases = append(shutdownPhases, "periodic-server-complete")
			shutdownTimes = append(shutdownTimes, time.Now())
			mu.Unlock()
			return nil
		case <-ctx.Done():
			mu.Lock()
			shutdownPhases = append(shutdownPhases, "periodic-server-timeout")
			shutdownTimes = append(shutdownTimes, time.Now())
			mu.Unlock()
			return ctx.Err()
		}
	})

	shutdownManager.Register("database", shutdown.PriorityLow, shutdown.TimeoutDatabase, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "database")
		shutdownTimes = append(shutdownTimes, time.Now())
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("tracer", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "tracer")
		shutdownTimes = append(shutdownTimes, time.Now())
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("completion", shutdown.PriorityLast, shutdown.TimeoutCompletion, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "completion")
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

	// Verify shutdown phases
	require.NotEmpty(t, shutdownPhases)
	assert.Equal(t, "periodic-server-start", shutdownPhases[0], "Periodic server should start shutdown first")
	assert.Equal(t, "completion", shutdownPhases[len(shutdownPhases)-1], "Completion should be last")

	// Verify periodic server completed gracefully (not timeout)
	assert.Contains(t, shutdownPhases, "periodic-server-complete", "Periodic server should complete gracefully")
	assert.NotContains(t, shutdownPhases, "periodic-server-timeout", "Periodic server should not timeout")

	// Verify total shutdown time allows for periodic task completion
	assert.GreaterOrEqual(t, totalDuration, shutdown.TimeoutTestFast, "Should wait for periodic tasks to complete")
	assert.Less(t, totalDuration, 500*time.Millisecond, "Should not take excessively long")
}

func TestPeriodicService_ExtendedTimeout(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := shutdown.NewShutdownManager(logger)

	var timeoutOccurred bool
	var mu sync.Mutex

	// Mock periodic server that takes longer than normal timeout but within extended timeout
	shutdownManager.Register("periodic-server", shutdown.PriorityHigh, shutdown.TimeoutTestStandard, func(ctx context.Context) error {
		select {
		case <-time.After(150 * time.Millisecond): // Just within timeout
			return nil
		case <-ctx.Done():
			mu.Lock()
			timeoutOccurred = true
			mu.Unlock()
			return ctx.Err()
		}
	})

	start := time.Now()
	ctx := context.Background()
	err := shutdownManager.Shutdown(ctx)
	duration := time.Since(start)

	// Should complete successfully without timeout
	require.NoError(t, err)

	mu.Lock()
	assert.False(t, timeoutOccurred, "Should not timeout with extended timeout")
	mu.Unlock()

	assert.GreaterOrEqual(t, duration, 150*time.Millisecond, "Should wait for periodic tasks")
	assert.Less(t, duration, 250*time.Millisecond, "Should complete within extended timeout")
}

func TestPeriodicService_ForceTimeout(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := shutdown.NewShutdownManager(logger)

	var timeoutOccurred bool
	var mu sync.Mutex

	// Mock periodic server that exceeds even the extended timeout
	shutdownManager.Register("periodic-server", shutdown.PriorityHigh, shutdown.TimeoutTestFast, func(ctx context.Context) error {
		select {
		case <-time.After(500 * time.Millisecond): // Exceeds timeout
			return nil
		case <-ctx.Done():
			mu.Lock()
			timeoutOccurred = true
			mu.Unlock()
			return ctx.Err()
		}
	})

	start := time.Now()
	ctx := context.Background()
	err := shutdownManager.Shutdown(ctx)
	duration := time.Since(start)

	// Should report error due to timeout
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown completed with")

	mu.Lock()
	assert.True(t, timeoutOccurred, "Should timeout when tasks take too long")
	mu.Unlock()

	assert.GreaterOrEqual(t, duration, shutdown.TimeoutTestFast, "Should respect timeout")
	assert.Less(t, duration, shutdown.TimeoutTestStandard, "Should not wait indefinitely")
}

func TestPeriodicService_PriorityOrder(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := shutdown.NewShutdownManager(logger)

	var shutdownOrder []string
	var mu sync.Mutex

	// Register components in different order than shutdown priority
	shutdownManager.Register("database", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error {
		mu.Lock()
		shutdownOrder = append(shutdownOrder, "database")
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("periodic-server", shutdown.PriorityHigh, shutdown.TimeoutStandard, func(ctx context.Context) error {
		mu.Lock()
		shutdownOrder = append(shutdownOrder, "periodic-server")
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("tracer", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error {
		mu.Lock()
		shutdownOrder = append(shutdownOrder, "tracer")
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("completion", shutdown.PriorityLast, shutdown.TimeoutCompletion, func(ctx context.Context) error {
		mu.Lock()
		shutdownOrder = append(shutdownOrder, "completion")
		mu.Unlock()
		return nil
	})

	ctx := context.Background()
	err := shutdownManager.Shutdown(ctx)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	// Verify correct priority order
	expected := []string{"periodic-server", "database", "tracer", "completion"}
	assert.Equal(t, expected, shutdownOrder, "Components should shut down in priority order")
}

// Test simulating real periodic task scenarios
func TestPeriodicService_TaskCompletion(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := shutdown.NewShutdownManager(logger)

	var tasksCompleted int
	var mu sync.Mutex

	// Simulate periodic server with multiple tasks
	shutdownManager.Register("periodic-server", shutdown.PriorityHigh, shutdown.TimeoutQuick, func(ctx context.Context) error {
		// Simulate 3 periodic tasks that need to complete
		for i := 0; i < 3; i++ {
			select {
			case <-time.After(50 * time.Millisecond): // Each task takes 50ms
				mu.Lock()
				tasksCompleted++
				mu.Unlock()
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})

	start := time.Now()
	ctx := context.Background()
	err := shutdownManager.Shutdown(ctx)
	duration := time.Since(start)

	require.NoError(t, err)

	mu.Lock()
	assert.Equal(t, 3, tasksCompleted, "All periodic tasks should complete")
	mu.Unlock()

	assert.GreaterOrEqual(t, duration, 150*time.Millisecond, "Should wait for all tasks to complete")
	assert.Less(t, duration, 300*time.Millisecond, "Should complete efficiently")
}

// Benchmark periodic service shutdown
func BenchmarkPeriodicService_Shutdown(b *testing.B) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		shutdownManager := shutdown.NewShutdownManager(logger)

		shutdownManager.Register("periodic-server", shutdown.PriorityHigh, shutdown.TimeoutTestFast, func(ctx context.Context) error {
			time.Sleep(10 * time.Millisecond) // Simulate brief task
			return nil
		})
		shutdownManager.Register("database", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error { return nil })
		shutdownManager.Register("tracer", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error { return nil })

		ctx := context.Background()
		_ = shutdownManager.Shutdown(ctx)
	}
}
