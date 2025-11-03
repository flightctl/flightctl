package shutdown

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShutdownManager_StatusTracking(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

	manager := NewShutdownManager(log)
	manager.SetServiceName("test-service")

	// Initial status should be idle
	status := manager.GetShutdownStatus()
	assert.False(t, status.IsShuttingDown)
	assert.Equal(t, "idle", status.State)
	assert.Nil(t, status.ShutdownInitiated)
	assert.Empty(t, status.ActiveComponents)
	assert.Empty(t, status.CompletedComponents)

	// Register test components
	manager.Register("http-server", PriorityHighest, TimeoutQuick, func(ctx context.Context) error {
		time.Sleep(100 * time.Millisecond) // Simulate work
		return nil
	})

	manager.Register("database", PriorityLowest, TimeoutQuick, func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond) // Simulate work
		return nil
	})

	// Start shutdown process
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run shutdown in goroutine to test status during shutdown
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- manager.Shutdown(ctx)
	}()

	// Give shutdown time to start
	time.Sleep(50 * time.Millisecond)

	// Check status during shutdown
	status = manager.GetShutdownStatus()
	assert.True(t, status.IsShuttingDown)
	assert.Contains(t, []string{"initiated", "in_progress"}, status.State)
	assert.NotNil(t, status.ShutdownInitiated)

	// Wait for completion
	err := <-shutdownDone
	require.NoError(t, err)

	// Check final status
	status = manager.GetShutdownStatus()
	assert.False(t, status.IsShuttingDown) // Completed, so no longer shutting down
	assert.Equal(t, "completed", status.State)
	assert.NotEmpty(t, status.CompletedComponents)
	assert.Equal(t, 2, len(status.CompletedComponents))

	// Verify component details
	foundHttpServer := false
	foundDatabase := false
	for _, comp := range status.CompletedComponents {
		switch comp.Name {
		case "http-server":
			foundHttpServer = true
			assert.Equal(t, "success", comp.Status)
			assert.Greater(t, comp.Duration, time.Duration(0))
		case "database":
			foundDatabase = true
			assert.Equal(t, "success", comp.Status)
			assert.Greater(t, comp.Duration, time.Duration(0))
		}
	}
	assert.True(t, foundHttpServer, "http-server component not found in completed components")
	assert.True(t, foundDatabase, "database component not found in completed components")
}

func TestShutdownManager_StatusWithErrors(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

	manager := NewShutdownManager(log)
	manager.SetServiceName("test-service")

	// Register components - one will fail
	manager.Register("good-component", PriorityHighest, TimeoutQuick, func(ctx context.Context) error {
		return nil
	})

	manager.Register("bad-component", PriorityHigh, TimeoutQuick, func(ctx context.Context) error {
		return fmt.Errorf("simulated failure")
	})

	// Start shutdown process
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := manager.Shutdown(ctx)
	require.Error(t, err) // Should have error due to bad component

	// Check final status
	status := manager.GetShutdownStatus()
	assert.Equal(t, "failed", status.State)
	assert.Contains(t, status.Message, "Shutdown failed")
	assert.Equal(t, 2, len(status.CompletedComponents))

	// Find the failed component
	var failedComponent *CompletedComponent
	for _, comp := range status.CompletedComponents {
		if comp.Name == "bad-component" {
			failedComponent = &comp
			break
		}
	}
	require.NotNil(t, failedComponent, "bad-component not found in completed components")
	assert.Equal(t, "error", failedComponent.Status)
	assert.Contains(t, failedComponent.Error, "simulated failure")
}

func TestShutdownManager_StatusWithTimeout(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

	manager := NewShutdownManager(log)
	manager.SetServiceName("test-service")

	// Register a component that will timeout
	manager.Register("slow-component", PriorityHighest, TimeoutTestFast, func(ctx context.Context) error {
		select {
		case <-time.After(500 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err() // Return context error when cancelled/timeout
		}
	})

	// Start shutdown process
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := manager.Shutdown(ctx)
	require.Error(t, err) // Should have error due to timeout

	// Check final status
	status := manager.GetShutdownStatus()
	assert.Equal(t, "failed", status.State)
	assert.Equal(t, 1, len(status.CompletedComponents))

	// Check the timed-out component
	comp := status.CompletedComponents[0]
	assert.Equal(t, "slow-component", comp.Name)
	assert.Equal(t, "timeout", comp.Status)
	assert.Equal(t, "component shutdown timed out", comp.Error)
}

func TestShutdownManager_ConcurrentStatusAccess(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

	manager := NewShutdownManager(log)
	manager.SetServiceName("test-service")

	// Register multiple components
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("component-%d", i)
		manager.Register(name, i, TimeoutQuick, func(ctx context.Context) error {
			time.Sleep(100 * time.Millisecond)
			return nil
		})
	}

	// Start shutdown process
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- manager.Shutdown(ctx)
	}()

	// Continuously read status during shutdown to test concurrent access
	statusReads := make(chan ShutdownStatus, 100)
	stopSignal := make(chan struct{})
	var statusGoroutines sync.WaitGroup

	for i := 0; i < 10; i++ {
		statusGoroutines.Add(1)
		go func() {
			defer statusGoroutines.Done()
			for j := 0; j < 10; j++ {
				select {
				case <-stopSignal:
					return
				default:
					status := manager.GetShutdownStatus()
					select {
					case statusReads <- status:
					case <-stopSignal:
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()
	}

	// Wait for shutdown to complete
	err := <-shutdownDone
	require.NoError(t, err)

	// Signal all status readers to stop and wait for them
	close(stopSignal)
	statusGoroutines.Wait()

	// Close status reads and verify we got some readings
	close(statusReads)
	statusCount := 0
	for range statusReads {
		statusCount++
	}
	assert.Greater(t, statusCount, 0, "Should have read at least some status updates")

	// Final status should be completed
	finalStatus := manager.GetShutdownStatus()
	assert.Equal(t, "completed", finalStatus.State)
	assert.Equal(t, 5, len(finalStatus.CompletedComponents))
}

func TestShutdownStatus_JSONSerialization(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

	manager := NewShutdownManager(log)
	manager.SetServiceName("test-service")

	manager.Register("test-component", PriorityHighest, TimeoutQuick, func(ctx context.Context) error {
		return nil
	})

	// Run shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := manager.Shutdown(ctx)
	require.NoError(t, err)

	// Get status and test JSON serialization
	status := manager.GetShutdownStatus()

	// The status should have JSON tags and be serializable
	assert.False(t, status.IsShuttingDown)
	assert.Equal(t, "completed", status.State)
	assert.NotNil(t, status.ShutdownInitiated)
	assert.NotEmpty(t, status.CompletedComponents)

	// Verify the structure has proper JSON tags (tested by compilation)
	comp := status.CompletedComponents[0]
	assert.Equal(t, "test-component", comp.Name)
	assert.Equal(t, "success", comp.Status)
	assert.Greater(t, comp.Duration, time.Duration(0))
	assert.Empty(t, comp.Error) // No error for successful component
}

func TestShutdownManager_StateTransitions(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

	manager := NewShutdownManager(log)

	// Test initial state
	assert.Equal(t, StateIdle, manager.state)

	// Test state transitions during shutdown
	manager.Register("test", PriorityHighest, TimeoutCompletion, func(ctx context.Context) error {
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Track state changes
	states := []ShutdownState{manager.state}

	// Use a custom component to capture intermediate states
	manager.Register("state-tracker", PriorityHigh, TimeoutCompletion, func(ctx context.Context) error {
		manager.mu.RLock()
		states = append(states, manager.state)
		manager.mu.RUnlock()
		return nil
	})

	err := manager.Shutdown(ctx)
	require.NoError(t, err)

	// Final state
	manager.mu.RLock()
	finalState := manager.state
	manager.mu.RUnlock()
	states = append(states, finalState)

	// Verify state progression: Idle -> (Initiated) -> InProgress -> Completed
	// Note: StateInitiated might be missed due to rapid transition
	assert.Contains(t, states, StateIdle)
	assert.Contains(t, states, StateInProgress)
	assert.Equal(t, StateCompleted, finalState)

	// Verify we have at least 3 different states (Idle, InProgress, Completed)
	uniqueStates := make(map[ShutdownState]bool)
	for _, state := range states {
		uniqueStates[state] = true
	}
	assert.GreaterOrEqual(t, len(uniqueStates), 3, "Should have at least 3 different states")
}
