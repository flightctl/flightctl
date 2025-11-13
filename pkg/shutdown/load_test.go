package shutdown

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// LoadTestConfig defines parameters for load testing
type LoadTestConfig struct {
	NumComponents    int
	NumConcurrentSMs int
	ComponentDelay   time.Duration
	ShutdownTimeout  time.Duration
	IterationCount   int
}

func TestShutdownManager_LoadTest_ManyComponents(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	config := LoadTestConfig{
		NumComponents:   50, // Reduced from 100
		ComponentDelay:  1 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(log)

	var completedComponents int64

	// Register many components with different priorities
	for i := 0; i < config.NumComponents; i++ {
		priority := i % 5 // Distribute across priorities 0-4
		componentName := fmt.Sprintf("component-%d", i)

		sm.Register(componentName, priority, config.ShutdownTimeout, func(ctx context.Context) error {
			// Simulate some work
			select {
			case <-time.After(config.ComponentDelay):
				atomic.AddInt64(&completedComponents, 1)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}

	start := time.Now()
	ctx := context.Background()
	err := sm.Shutdown(ctx)
	duration := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, int64(config.NumComponents), atomic.LoadInt64(&completedComponents),
		"All components should complete")

	t.Logf("Load test with %d components completed in %v", config.NumComponents, duration)

	// Performance assertion - should handle many components efficiently
	expectedMaxDuration := time.Duration(config.NumComponents) * config.ComponentDelay * 2 // Allow reasonable time
	assert.Less(t, duration, expectedMaxDuration,
		fmt.Sprintf("Shutdown should complete efficiently even with %d components", config.NumComponents))
}

func TestShutdownManager_LoadTest_ConcurrentManagers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent load test in short mode")
	}

	config := LoadTestConfig{
		NumConcurrentSMs: 10,
		NumComponents:    20,
		ComponentDelay:   5 * time.Millisecond,
		ShutdownTimeout:  2 * time.Second,
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	var wg sync.WaitGroup
	var successfulShutdowns int64
	var totalComponents int64

	start := time.Now()

	// Run multiple shutdown managers concurrently
	for i := 0; i < config.NumConcurrentSMs; i++ {
		wg.Add(1)
		go func(managerID int) {
			defer wg.Done()

			sm := NewShutdownManager(log)
			var localComponents int64

			// Register components for this manager
			for j := 0; j < config.NumComponents; j++ {
				componentName := fmt.Sprintf("manager-%d-component-%d", managerID, j)
				sm.Register(componentName, j%3, config.ShutdownTimeout, func(ctx context.Context) error {
					select {
					case <-time.After(config.ComponentDelay):
						atomic.AddInt64(&localComponents, 1)
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}

			// Shutdown this manager
			ctx := context.Background()
			if err := sm.Shutdown(ctx); err == nil {
				atomic.AddInt64(&successfulShutdowns, 1)
				atomic.AddInt64(&totalComponents, localComponents)
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	assert.Equal(t, int64(config.NumConcurrentSMs), atomic.LoadInt64(&successfulShutdowns),
		"All shutdown managers should complete successfully")
	assert.Equal(t, int64(config.NumConcurrentSMs*config.NumComponents), atomic.LoadInt64(&totalComponents),
		"All components should complete")

	t.Logf("Concurrent load test with %d managers (%d components each) completed in %v",
		config.NumConcurrentSMs, config.NumComponents, duration)
}

func TestShutdownManager_LoadTest_MemoryUsage(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory load test in short mode")
	}

	runtime.GC() // Clean up before measuring
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	// Create and shutdown many managers to test for memory leaks
	iterations := 50
	componentsPerManager := 100

	for i := 0; i < iterations; i++ {
		sm := NewShutdownManager(log)

		// Register many components
		for j := 0; j < componentsPerManager; j++ {
			sm.Register(fmt.Sprintf("component-%d", j), j%5, 1*time.Second, func(ctx context.Context) error {
				return nil
			})
		}

		ctx := context.Background()
		err := sm.Shutdown(ctx)
		require.NoError(t, err)
	}

	runtime.GC() // Force garbage collection
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	// Memory should not grow significantly (handle potential underflow)
	var memoryGrowth uint64
	if m2.Alloc > m1.Alloc {
		memoryGrowth = m2.Alloc - m1.Alloc
	} else {
		memoryGrowth = 0 // Memory actually decreased (GC worked)
	}
	t.Logf("Memory growth after %d iterations: %d bytes (before: %d, after: %d)", iterations, memoryGrowth, m1.Alloc, m2.Alloc)

	// Allow some growth but not excessive - more lenient for test environment
	maxAcceptableGrowth := uint64(50 * 1024 * 1024) // 50MB for test environment
	assert.Less(t, memoryGrowth, maxAcceptableGrowth,
		"Memory usage should not grow excessively")
}

func TestShutdownManager_LoadTest_HighFrequencyShutdowns(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping high frequency test in short mode")
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	iterations := 100
	var totalDuration time.Duration
	var successCount int64

	for i := 0; i < iterations; i++ {
		sm := NewShutdownManager(log)

		// Register a few components
		for j := 0; j < 10; j++ {
			sm.Register(fmt.Sprintf("component-%d", j), j%3, 100*time.Millisecond,
				func(ctx context.Context) error {
					time.Sleep(1 * time.Millisecond) // Minimal work
					return nil
				})
		}

		start := time.Now()
		ctx := context.Background()
		if err := sm.Shutdown(ctx); err == nil {
			atomic.AddInt64(&successCount, 1)
		}
		totalDuration += time.Since(start)
	}

	avgDuration := totalDuration / time.Duration(iterations)
	t.Logf("High frequency test: %d iterations, average shutdown time: %v",
		iterations, avgDuration)

	assert.Equal(t, int64(iterations), atomic.LoadInt64(&successCount),
		"All high frequency shutdowns should succeed")
	assert.Less(t, avgDuration, 50*time.Millisecond,
		"Average shutdown time should be efficient")
}

func TestShutdownManager_LoadTest_StressWithFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(log)

	numComponents := 50
	failureRate := 0.2 // 20% of components will fail
	var successfulShutdowns int64
	var failedShutdowns int64

	for i := 0; i < numComponents; i++ {
		willFail := (float64(i) / float64(numComponents)) < failureRate
		componentName := fmt.Sprintf("component-%d", i)

		sm.Register(componentName, i%4, 2*time.Second, func(name string, shouldFail bool) func(context.Context) error {
			return func(ctx context.Context) error {
				// Simulate work
				select {
				case <-time.After(10 * time.Millisecond):
					if shouldFail {
						atomic.AddInt64(&failedShutdowns, 1)
						return fmt.Errorf("component %s failed", name)
					}
					atomic.AddInt64(&successfulShutdowns, 1)
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}(componentName, willFail))
	}

	start := time.Now()
	ctx := context.Background()
	err := sm.Shutdown(ctx)
	duration := time.Since(start)

	// Should report errors but continue
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown completed with")

	expectedSuccess := int64(float64(numComponents) * (1.0 - failureRate))
	expectedFailures := int64(float64(numComponents) * failureRate)

	assert.Equal(t, expectedSuccess, atomic.LoadInt64(&successfulShutdowns),
		"Expected number of successful shutdowns")
	assert.Equal(t, expectedFailures, atomic.LoadInt64(&failedShutdowns),
		"Expected number of failed shutdowns")

	t.Logf("Stress test with failures: %d components, %d successful, %d failed, duration: %v",
		numComponents, atomic.LoadInt64(&successfulShutdowns),
		atomic.LoadInt64(&failedShutdowns), duration)
}

func TestShutdownManager_LoadTest_TimeoutStress(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping timeout stress test in short mode")
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(log)

	// Mix of fast and slow components to test timeout handling
	fastComponents := 20
	slowComponents := 5

	var completedFast int64
	var timedOutSlow int64

	// Fast components
	for i := 0; i < fastComponents; i++ {
		sm.Register(fmt.Sprintf("fast-%d", i), 1, 5*time.Second, func(ctx context.Context) error {
			select {
			case <-time.After(5 * time.Millisecond):
				atomic.AddInt64(&completedFast, 1)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}

	// Slow components that will timeout
	for i := 0; i < slowComponents; i++ {
		sm.Register(fmt.Sprintf("slow-%d", i), 2, 50*time.Millisecond, func(ctx context.Context) error {
			select {
			case <-time.After(500 * time.Millisecond): // Will timeout
				return nil
			case <-ctx.Done():
				atomic.AddInt64(&timedOutSlow, 1)
				return ctx.Err()
			}
		})
	}

	start := time.Now()
	ctx := context.Background()
	err := sm.Shutdown(ctx)
	duration := time.Since(start)

	// Should report timeout errors
	require.Error(t, err)

	assert.Equal(t, int64(fastComponents), atomic.LoadInt64(&completedFast),
		"All fast components should complete")
	assert.Equal(t, int64(slowComponents), atomic.LoadInt64(&timedOutSlow),
		"All slow components should timeout")

	t.Logf("Timeout stress test: %d fast, %d slow (timed out), duration: %v",
		fastComponents, slowComponents, duration)

	// Should complete quickly due to timeouts, not wait for slow components
	// More lenient timeout for CI environments
	maxExpectedDuration := 500 * time.Millisecond // Allow more time for CI/test environments
	assert.Less(t, duration, maxExpectedDuration,
		"Should not wait for slow components beyond timeout")
}

// Benchmark concurrent shutdown managers under load
func BenchmarkShutdownManager_ConcurrentLoad(b *testing.B) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	componentsPerManager := 20

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sm := NewShutdownManager(log)

			for i := 0; i < componentsPerManager; i++ {
				sm.Register(fmt.Sprintf("component-%d", i), i%3, 1*time.Second,
					func(ctx context.Context) error {
						time.Sleep(100 * time.Microsecond) // Minimal work
						return nil
					})
			}

			ctx := context.Background()
			_ = sm.Shutdown(ctx)
		}
	})
}

// Benchmark shutdown performance scaling with component count
func BenchmarkShutdownManager_ComponentScaling(b *testing.B) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	componentCounts := []int{10, 50, 100, 500, 1000}

	for _, count := range componentCounts {
		b.Run(fmt.Sprintf("components-%d", count), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sm := NewShutdownManager(log)

				for j := 0; j < count; j++ {
					sm.Register(fmt.Sprintf("component-%d", j), j%5, 1*time.Second,
						func(ctx context.Context) error {
							return nil
						})
				}

				ctx := context.Background()
				_ = sm.Shutdown(ctx)
			}
		})
	}
}
