package shutdown

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShutdownManager_New(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

	sm := NewShutdownManager(log)
	assert.NotNil(t, sm)
	assert.Empty(t, sm.components)
	assert.False(t, sm.failFastEnabled)
	assert.Nil(t, sm.failFastCancel)
}

func TestShutdownManager_Register(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	sm := NewShutdownManager(log)

	callOrder := make([]string, 0)
	var mu sync.Mutex

	callback := func(name string) ShutdownCallback {
		return func(ctx context.Context) error {
			mu.Lock()
			callOrder = append(callOrder, name)
			mu.Unlock()
			return nil
		}
	}

	// Register components with different priorities
	sm.Register("high-priority", PriorityHighest, TimeoutStandard, callback("high-priority"))
	sm.Register("low-priority", PriorityLow, TimeoutStandard, callback("low-priority"))
	sm.Register("medium-priority", PriorityHigh, TimeoutStandard, callback("medium-priority"))

	assert.Len(t, sm.components, 3)
}

func TestShutdownManager_PriorityOrdering(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	sm := NewShutdownManager(log)

	callOrder := make([]string, 0)
	var mu sync.Mutex

	callback := func(name string) ShutdownCallback {
		return func(ctx context.Context) error {
			mu.Lock()
			callOrder = append(callOrder, name)
			mu.Unlock()
			return nil
		}
	}

	// Register components in random order but with specific priorities
	sm.Register("priority-3", PriorityLow, TimeoutStandard, callback("priority-3"))
	sm.Register("priority-0", PriorityHighest, TimeoutStandard, callback("priority-0"))
	sm.Register("priority-1", PriorityHigh, TimeoutStandard, callback("priority-1"))
	sm.Register("priority-2", PriorityNormal, TimeoutStandard, callback("priority-2"))

	ctx := context.Background()
	err := sm.Shutdown(ctx)
	require.NoError(t, err)

	// Verify components were called in priority order (lower numbers first)
	expected := []string{"priority-0", "priority-1", "priority-2", "priority-3"}
	mu.Lock()
	assert.Equal(t, expected, callOrder)
	mu.Unlock()
}

func TestShutdownManager_TimeoutHandling(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	sm := NewShutdownManager(log)

	// Component that takes longer than timeout
	sm.Register("slow-component", PriorityHighest, TimeoutTestFast, func(ctx context.Context) error {
		select {
		case <-time.After(500 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	start := time.Now()
	ctx := context.Background()
	err := sm.Shutdown(ctx)
	duration := time.Since(start)

	// Should complete within timeout + small buffer
	assert.Less(t, duration, TimeoutTestStandard)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown completed with")
}

func TestShutdownManager_ErrorCollection(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	sm := NewShutdownManager(log)

	// Components that return errors
	sm.Register("failing-1", PriorityHighest, TimeoutStandard, func(ctx context.Context) error {
		return errors.New("component 1 failed")
	})
	sm.Register("successful", PriorityHigh, TimeoutStandard, func(ctx context.Context) error {
		return nil
	})
	sm.Register("failing-2", PriorityNormal, TimeoutStandard, func(ctx context.Context) error {
		return errors.New("component 2 failed")
	})

	ctx := context.Background()
	err := sm.Shutdown(ctx)
	require.Error(t, err)

	// Should contain information about both failures
	assert.Contains(t, err.Error(), "2 errors")
	assert.Contains(t, err.Error(), "component 1 failed")
	assert.Contains(t, err.Error(), "component 2 failed")
}

func TestShutdownManager_FailFast(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	sm := NewShutdownManager(log)

	// Test fail-fast is initially disabled
	assert.False(t, sm.failFastEnabled)

	// Enable fail-fast
	ctx, cancel := context.WithCancel(context.Background())
	sm.EnableFailFast(cancel)
	assert.True(t, sm.failFastEnabled)
	assert.NotNil(t, sm.failFastCancel)

	// Trigger fail-fast
	testErr := errors.New("component failure")
	sm.TriggerFailFast("test-component", testErr)

	// Context should be cancelled
	select {
	case <-ctx.Done():
		// Expected - context was cancelled
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Expected context to be cancelled")
	}
}

func TestShutdownManager_FailFastDisabled(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	sm := NewShutdownManager(log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Don't enable fail-fast
	testErr := errors.New("component failure")
	sm.TriggerFailFast("test-component", testErr)

	// Context should not be cancelled
	select {
	case <-ctx.Done():
		t.Fatal("Context should not be cancelled when fail-fast is disabled")
	case <-time.After(50 * time.Millisecond):
		// Expected - context remains active
	}
}

func TestShutdownManager_ConcurrentRegister(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	sm := NewShutdownManager(log)

	// Test concurrent registration
	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sm.Register(fmt.Sprintf("component-%d", id), id, TimeoutStandard, func(ctx context.Context) error {
				return nil
			})
		}(i)
	}

	wg.Wait()
	assert.Len(t, sm.components, numGoroutines)
}

func TestShutdownManager_ShutdownWithCancelledContext(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	sm := NewShutdownManager(log)

	sm.Register("test-component", PriorityHighest, TimeoutStandard, func(ctx context.Context) error {
		return nil
	})

	// Create already cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sm.Shutdown(ctx)
	assert.NoError(t, err) // Should still work even with cancelled context
}

func TestShutdownManager_EmptyComponentsList(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	sm := NewShutdownManager(log)

	// No components registered
	ctx := context.Background()
	err := sm.Shutdown(ctx)
	assert.NoError(t, err)
}

func TestShutdownManager_ComponentPanicRecovery(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	sm := NewShutdownManager(log)

	// Component that panics
	sm.Register("panicking-component", PriorityHighest, TimeoutStandard, func(ctx context.Context) error {
		panic("component panic")
	})

	sm.Register("normal-component", PriorityHigh, TimeoutStandard, func(ctx context.Context) error {
		return nil
	})

	ctx := context.Background()

	// This should not panic - the shutdown manager should recover from component panics
	err := sm.Shutdown(ctx)

	// Shutdown should complete with an error because one component panicked
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "component panicked")
	assert.Contains(t, err.Error(), "panicking-component")
}

// Benchmark tests for performance under load
func BenchmarkShutdownManager_ManyComponents(b *testing.B) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm := NewShutdownManager(log)

		// Register many components
		priorities := []int{PriorityHighest, PriorityHigh, PriorityNormal, PriorityLow, PriorityLowest}
		for j := 0; j < 100; j++ {
			sm.Register(fmt.Sprintf("component-%d", j), priorities[j%5], TimeoutStandard, func(ctx context.Context) error {
				return nil
			})
		}

		ctx := context.Background()
		_ = sm.Shutdown(ctx)
	}
}

// Test helper function to create a test logger that captures logs
func createTestLogger() (*logrus.Logger, *TestLogHook) {
	log := logrus.New()
	hook := &TestLogHook{entries: make([]*logrus.Entry, 0)}
	log.AddHook(hook)
	log.SetLevel(logrus.DebugLevel)
	return log, hook
}

type TestLogHook struct {
	entries []*logrus.Entry
	mu      sync.Mutex
}

func (h *TestLogHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *TestLogHook) Fire(entry *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, entry)
	return nil
}

func (h *TestLogHook) GetEntries() []*logrus.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*logrus.Entry(nil), h.entries...)
}

func TestShutdownManager_LoggingBehavior(t *testing.T) {
	log, hook := createTestLogger()
	sm := NewShutdownManager(log)

	sm.Register("test-component", PriorityHighest, TimeoutStandard, func(ctx context.Context) error {
		return nil
	})

	ctx := context.Background()
	err := sm.Shutdown(ctx)
	require.NoError(t, err)

	entries := hook.GetEntries()
	assert.GreaterOrEqual(t, len(entries), 2) // At least start and completion logs

	// Check for expected log messages
	var foundStartLog, foundCompletionLog bool
	for _, entry := range entries {
		if entry.Message == "Starting component shutdown" {
			foundStartLog = true
			assert.Equal(t, "test-component", entry.Data["component"])
		}
		if entry.Message == "Component shutdown completed successfully" {
			foundCompletionLog = true
		}
	}

	assert.True(t, foundStartLog, "Should log component shutdown start")
	assert.True(t, foundCompletionLog, "Should log component shutdown completion")
}
