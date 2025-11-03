package main

import (
	"context"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIService_GracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// This test validates the API service can start and shutdown gracefully
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := shutdown.NewShutdownManager(logger)

	// Create a context for fail-fast behavior
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownManager.EnableFailFast(cancel)

	// Track shutdown phases
	var shutdownPhases []string
	var mu sync.Mutex

	// Mock components that simulate the real API service components
	shutdownManager.Register("servers", shutdown.PriorityHighest, shutdown.TimeoutStandard, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "servers")
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("org-cache", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "org-cache")
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("database", shutdown.PriorityLowest, shutdown.TimeoutDatabase, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "database")
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("kvstore", shutdown.PriorityLowest, shutdown.TimeoutStandard, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "kvstore")
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("queues", shutdown.PriorityLowest, shutdown.TimeoutDatabase, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "queues")
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("tracer", shutdown.PriorityLowest, shutdown.TimeoutStandard, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "tracer")
		mu.Unlock()
		return nil
	})

	shutdownManager.Register("completion", shutdown.PriorityLast, shutdown.TimeoutCompletion, func(ctx context.Context) error {
		mu.Lock()
		shutdownPhases = append(shutdownPhases, "completion")
		mu.Unlock()
		return nil
	})

	// Start signal handling
	shutdown.HandleSignalsWithManager(logger, shutdownManager, shutdown.DefaultGracefulShutdownTimeout)

	// Give signal handler time to setup
	time.Sleep(10 * time.Millisecond)

	// Trigger shutdown via signal
	err := syscall.Kill(os.Getpid(), syscall.SIGTERM)
	require.NoError(t, err)

	// Wait for shutdown to complete
	time.Sleep(500 * time.Millisecond)

	// Verify shutdown order
	mu.Lock()
	defer mu.Unlock()

	require.NotEmpty(t, shutdownPhases, "Shutdown phases should be recorded")

	// Verify servers are shut down first (priority 0)
	assert.Equal(t, "servers", shutdownPhases[0], "Servers should shut down first")

	// Verify completion is last (priority 5)
	assert.Equal(t, "completion", shutdownPhases[len(shutdownPhases)-1], "Completion should be last")

	// Verify org-cache comes before database/kvstore/queues/tracer
	var orgCacheIdx, databaseIdx int = -1, -1
	for i, phase := range shutdownPhases {
		if phase == "org-cache" {
			orgCacheIdx = i
		}
		if phase == "database" {
			databaseIdx = i
		}
	}
	assert.True(t, orgCacheIdx < databaseIdx, "org-cache (priority 3) should come before database (priority 4)")
}

func TestAPIService_FailFastBehavior(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := shutdown.NewShutdownManager(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownManager.EnableFailFast(cancel)

	// Simulate server failure scenario
	var failFastTriggered bool
	var mu sync.Mutex

	// Start a goroutine that simulates a server failure
	go func() {
		time.Sleep(10 * time.Millisecond) // Simulate server running briefly
		shutdownManager.TriggerFailFast("api-server", assert.AnError)
		mu.Lock()
		failFastTriggered = true
		mu.Unlock()
	}()

	// Wait for fail-fast to trigger
	select {
	case <-ctx.Done():
		// Expected - fail-fast should trigger context cancellation
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Fail-fast should have triggered context cancellation")
	}

	mu.Lock()
	assert.True(t, failFastTriggered, "Fail-fast should have been triggered")
	mu.Unlock()
}

func TestAPIService_ComponentTimeouts(t *testing.T) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	shutdownManager := shutdown.NewShutdownManager(logger)

	// Add component that exceeds its timeout
	shutdownManager.Register("slow-component", shutdown.PriorityHighest, shutdown.TimeoutTestFast, func(ctx context.Context) error {
		select {
		case <-time.After(500 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	// Add normal component
	shutdownManager.Register("fast-component", shutdown.PriorityHigh, shutdown.TimeoutStandard, func(ctx context.Context) error {
		return nil
	})

	start := time.Now()
	ctx := context.Background()
	err := shutdownManager.Shutdown(ctx)
	duration := time.Since(start)

	// Should complete within reasonable time despite slow component
	assert.Less(t, duration, shutdown.TimeoutTestStandard, "Should not wait indefinitely for slow components")

	// Should report errors from timeout
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown completed with")
}

// Benchmark test for API service shutdown performance
func BenchmarkAPIService_Shutdown(b *testing.B) {
	logger := log.InitLogs()
	logger.SetLevel(logrus.ErrorLevel)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		shutdownManager := shutdown.NewShutdownManager(logger)

		// Simulate API service components
		shutdownManager.Register("servers", shutdown.PriorityHighest, shutdown.TimeoutStandard, func(ctx context.Context) error { return nil })
		shutdownManager.Register("org-cache", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error { return nil })
		shutdownManager.Register("membership-cache", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error { return nil })
		shutdownManager.Register("database", shutdown.PriorityLowest, shutdown.TimeoutDatabase, func(ctx context.Context) error { return nil })
		shutdownManager.Register("kvstore", shutdown.PriorityLowest, shutdown.TimeoutStandard, func(ctx context.Context) error { return nil })
		shutdownManager.Register("queues", shutdown.PriorityLowest, shutdown.TimeoutDatabase, func(ctx context.Context) error { return nil })
		shutdownManager.Register("tracer", shutdown.PriorityLowest, shutdown.TimeoutStandard, func(ctx context.Context) error { return nil })

		ctx := context.Background()
		_ = shutdownManager.Shutdown(ctx)
	}
}
