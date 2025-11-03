package shutdown

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

// ChaosConfig defines parameters for chaos testing
type ChaosConfig struct {
	ComponentCount     int           // Number of components to register
	MaxDelay           time.Duration // Maximum random delay
	FailureRate        float64       // Probability of component failure (0.0-1.0)
	TimeoutRate        float64       // Probability of component timeout (0.0-1.0)
	PanicRate          float64       // Probability of component panic (0.0-1.0)
	ConcurrentManagers int           // Number of concurrent shutdown managers
	StressIterations   int           // Number of stress test iterations
}

// DefaultChaosConfig returns sensible defaults for chaos testing
func DefaultChaosConfig() ChaosConfig {
	return ChaosConfig{
		ComponentCount:     10,
		MaxDelay:           500 * time.Millisecond,
		FailureRate:        0.2,  // 20% failure rate
		TimeoutRate:        0.1,  // 10% timeout rate
		PanicRate:          0.05, // 5% panic rate
		ConcurrentManagers: 5,
		StressIterations:   50,
	}
}

// ChaosComponent represents a component that can exhibit chaotic behavior
type ChaosComponent struct {
	Name        string
	Priority    int
	Timeout     time.Duration
	Config      ChaosConfig
	CallCount   int64
	FailureMode string // "success", "failure", "timeout", "panic", "delay"
}

// NewChaosComponent creates a component with chaotic behavior
func NewChaosComponent(name string, priority int, timeout time.Duration, config ChaosConfig) *ChaosComponent {
	return &ChaosComponent{
		Name:     name,
		Priority: priority,
		Timeout:  timeout,
		Config:   config,
	}
}

// Execute simulates component shutdown with chaotic behavior
func (c *ChaosComponent) Execute(ctx context.Context) error {
	atomic.AddInt64(&c.CallCount, 1)

	// Randomly determine failure mode
	rnd := rand.Float64() //nolint:gosec // G404: Using math/rand for chaos testing is appropriate

	switch {
	case rnd < c.Config.PanicRate:
		c.FailureMode = "panic"
		panic(fmt.Sprintf("chaos panic in component %s", c.Name))

	case rnd < c.Config.PanicRate+c.Config.FailureRate:
		c.FailureMode = "failure"
		return fmt.Errorf("chaos failure in component %s", c.Name)

	case rnd < c.Config.PanicRate+c.Config.FailureRate+c.Config.TimeoutRate:
		c.FailureMode = "timeout"
		// Simulate work longer than timeout
		select {
		case <-time.After(c.Timeout + 100*time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}

	default:
		c.FailureMode = "success"
		// Add random delay
		if c.Config.MaxDelay > 0 {
			delay := time.Duration(rand.Int63n(int64(c.Config.MaxDelay))) //nolint:gosec // G404: Using math/rand for chaos testing is appropriate
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
}

// TestShutdownManager_ChaosBasic tests basic chaos scenarios
func TestShutdownManager_ChaosBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos test in short mode")
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise

	config := DefaultChaosConfig()
	config.ComponentCount = 5
	config.FailureRate = 0.3 // Higher failure rate for testing

	for iteration := 0; iteration < 10; iteration++ {
		t.Run(fmt.Sprintf("iteration_%d", iteration), func(t *testing.T) {
			sm := NewShutdownManager(log)

			var components []*ChaosComponent

			// Register chaos components
			for i := 0; i < config.ComponentCount; i++ {
				comp := NewChaosComponent(
					fmt.Sprintf("chaos-comp-%d", i),
					rand.Intn(6), //nolint:gosec // G404: Using math/rand for chaos testing is appropriate
					TimeoutTestStandard,
					config,
				)
				components = append(components, comp)

				// Capture comp in closure
				component := comp
				sm.Register(component.Name, component.Priority, component.Timeout, func(ctx context.Context) error {
					return component.Execute(ctx)
				})
			}

			// Execute shutdown and expect it to complete despite chaos
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			start := time.Now()
			err := sm.Shutdown(ctx)
			duration := time.Since(start)

			// Verify shutdown completed within reasonable time
			assert.Less(t, duration, 8*time.Second, "Chaos shutdown should complete within reasonable time")

			// Count components that were called
			calledCount := 0
			for _, comp := range components {
				if atomic.LoadInt64(&comp.CallCount) > 0 {
					calledCount++
				}
			}

			// All components should have been attempted
			assert.Equal(t, config.ComponentCount, calledCount, "All components should be called during shutdown")

			// Shutdown might fail due to chaos, but should not hang
			if err != nil {
				t.Logf("Chaos shutdown failed as expected: %v", err)
			}
		})
	}
}

// TestShutdownManager_ChaosStress tests shutdown under high stress
func TestShutdownManager_ChaosStress(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos stress test in short mode")
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	config := DefaultChaosConfig()
	config.ComponentCount = 5               // Reduce to 5 components
	config.FailureRate = 0.05               // Reduce to 5% failure rate
	config.TimeoutRate = 0.02               // Reduce to 2% timeout rate
	config.PanicRate = 0.005                // Reduce to 0.5% panic rate
	config.MaxDelay = 20 * time.Millisecond // Reduce delay
	config.StressIterations = 25

	var successCount, failureCount int64
	var totalDuration time.Duration
	var mu sync.Mutex

	// Run multiple concurrent shutdown managers
	var wg sync.WaitGroup
	for i := 0; i < config.ConcurrentManagers; i++ {
		wg.Add(1)
		go func(managerID int) {
			defer wg.Done()

			for iteration := 0; iteration < config.StressIterations/config.ConcurrentManagers; iteration++ {
				sm := NewShutdownManager(log)

				// Register many chaos components
				for j := 0; j < config.ComponentCount; j++ {
					comp := NewChaosComponent(
						fmt.Sprintf("stress-comp-%d-%d", managerID, j),
						j%6, // Cycle through priorities
						TimeoutTestFast,
						config,
					)

					component := comp
					sm.Register(component.Name, component.Priority, component.Timeout, func(ctx context.Context) error {
						return component.Execute(ctx)
					})
				}

				// Execute shutdown with reasonable timeout to allow some success
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				start := time.Now()
				err := sm.Shutdown(ctx)
				duration := time.Since(start)
				cancel()

				mu.Lock()
				totalDuration += duration
				if err != nil {
					atomic.AddInt64(&failureCount, 1)
				} else {
					atomic.AddInt64(&successCount, 1)
				}
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	totalTests := successCount + failureCount
	successRate := float64(successCount) / float64(totalTests)
	avgDuration := totalDuration / time.Duration(totalTests)

	t.Logf("Chaos stress test results:")
	t.Logf("  Total tests: %d", totalTests)
	t.Logf("  Success rate: %.2f%%", successRate*100)
	t.Logf("  Average duration: %v", avgDuration)

	// Even under stress, some shutdowns should succeed
	assert.GreaterOrEqual(t, successRate, 0.1, "At least 10%% of shutdowns should succeed even under chaos")
	assert.Less(t, avgDuration, 3*time.Second, "Average shutdown duration should be reasonable")
	assert.Greater(t, totalTests, int64(10), "Should have run a reasonable number of tests")
}

// TestShutdownManager_ChaosRaceConditions tests race conditions during shutdown
func TestShutdownManager_ChaosRaceConditions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos race condition test in short mode")
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	for iteration := 0; iteration < 20; iteration++ {
		t.Run(fmt.Sprintf("race_iteration_%d", iteration), func(t *testing.T) {
			sm := NewShutdownManager(log)

			// Enable fail-fast for additional chaos
			ctx, cancel := context.WithCancel(context.Background())
			sm.EnableFailFast(cancel)

			var registrationWg sync.WaitGroup
			var shutdownWg sync.WaitGroup

			// Concurrently register components while shutdown might be starting
			registrationWg.Add(1)
			go func() {
				defer registrationWg.Done()
				for i := 0; i < 10; i++ {
					comp := NewChaosComponent(
						fmt.Sprintf("race-comp-%d", i),
						rand.Intn(6), //nolint:gosec // G404: Using math/rand for chaos testing is appropriate
						TimeoutTestFast,
						DefaultChaosConfig(),
					)

					component := comp
					sm.Register(component.Name, component.Priority, component.Timeout, func(ctx context.Context) error {
						// Introduce random delays and failures
						if rand.Float64() < 0.3 { //nolint:gosec // G404: Using math/rand for chaos testing is appropriate
							sm.TriggerFailFast(component.Name, errors.New("chaos failure"))
						}
						return component.Execute(ctx)
					})

					// Small random delay between registrations
					time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond) //nolint:gosec // G404: Using math/rand for chaos testing is appropriate
				}
			}()

			// Start shutdown concurrently with registration
			shutdownWg.Add(1)
			go func() {
				defer shutdownWg.Done()
				// Small delay to let some components register
				time.Sleep(time.Duration(rand.Intn(50)) * time.Millisecond) //nolint:gosec // G404: Using math/rand for chaos testing is appropriate

				shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 2*time.Second)
				defer shutdownCancel()

				err := sm.Shutdown(shutdownCtx)
				if err != nil {
					t.Logf("Race condition shutdown failed: %v", err)
				}
			}()

			// Wait for registration to complete
			registrationWg.Wait()

			// Trigger random fail-fast events
			go func() {
				time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond) //nolint:gosec // G404: Using math/rand for chaos testing is appropriate
				if rand.Float64() < 0.5 {                                    //nolint:gosec // G404: Using math/rand for chaos testing is appropriate
					sm.TriggerFailFast("external-trigger", errors.New("external chaos"))
				}
			}()

			// Wait for shutdown to complete
			shutdownWg.Wait()

			// Test should complete without deadlocks or panics
		})
	}
}

// TestShutdownManager_ChaosMemoryPressure tests shutdown under memory pressure
func TestShutdownManager_ChaosMemoryPressure(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos memory pressure test in short mode")
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	// Create memory pressure by allocating large amounts of memory
	var memoryPressure [][]byte
	defer func() {
		// Clean up memory
		memoryPressure = nil
	}()

	// Allocate memory in chunks to create pressure
	for i := 0; i < 100; i++ {
		chunk := make([]byte, 1024*1024) // 1MB chunks
		memoryPressure = append(memoryPressure, chunk)
	}

	sm := NewShutdownManager(log)

	// Register components that allocate and release memory
	for i := 0; i < 15; i++ {
		componentID := i
		sm.Register(
			fmt.Sprintf("memory-comp-%d", componentID),
			componentID%6,
			TimeoutStandard,
			func(ctx context.Context) error {
				// Allocate memory during shutdown
				var localMemory [][]byte
				for j := 0; j < 50; j++ {
					localMemory = append(localMemory, make([]byte, 512*1024)) //nolint:staticcheck // SA4010: Memory allocation for testing memory pressure

					// Check for context cancellation
					select {
					case <-ctx.Done():
						return ctx.Err()
					default:
					}
				}

				// Simulate work
				time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond) //nolint:gosec // G404: Using math/rand for chaos testing is appropriate

				// Memory will be released when function exits
				return nil
			},
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := sm.Shutdown(ctx)
	duration := time.Since(start)

	// Shutdown should complete even under memory pressure
	assert.Less(t, duration, 8*time.Second, "Shutdown should complete under memory pressure")
	if err != nil {
		t.Logf("Memory pressure shutdown result: %v", err)
	}
}

// TestShutdownManager_ChaosSignalStorm tests handling of multiple rapid signals
func TestShutdownManager_ChaosSignalStorm(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos signal storm test in short mode")
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	for stormIteration := 0; stormIteration < 5; stormIteration++ {
		t.Run(fmt.Sprintf("storm_%d", stormIteration), func(t *testing.T) {
			sm := NewShutdownManager(log)

			// Register components with varying behavior
			for i := 0; i < 8; i++ {
				comp := NewChaosComponent(
					fmt.Sprintf("signal-comp-%d", i),
					i%6,
					TimeoutStandard,
					DefaultChaosConfig(),
				)

				component := comp
				sm.Register(component.Name, component.Priority, component.Timeout, func(ctx context.Context) error {
					return component.Execute(ctx)
				})
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Create "signal storm" by triggering multiple fail-fast events
			sm.EnableFailFast(cancel)

			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond) //nolint:gosec // G404: Using math/rand for chaos testing is appropriate
					sm.TriggerFailFast(fmt.Sprintf("storm-trigger-%d", id),
						fmt.Errorf("signal storm event %d", id))
				}(i)
			}

			// Start shutdown
			shutdownDone := make(chan error, 1)
			go func() {
				shutdownDone <- sm.Shutdown(ctx)
			}()

			// Wait for storm triggers
			wg.Wait()

			// Wait for shutdown to complete
			select {
			case err := <-shutdownDone:
				if err != nil {
					t.Logf("Signal storm shutdown result: %v", err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("Shutdown did not complete within timeout during signal storm")
			}
		})
	}
}

// BenchmarkShutdownManager_ChaosPerformance benchmarks shutdown performance under chaos
func BenchmarkShutdownManager_ChaosPerformance(b *testing.B) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	config := ChaosConfig{
		ComponentCount: 15,
		MaxDelay:       50 * time.Millisecond,
		FailureRate:    0.2,
		TimeoutRate:    0.1,
		PanicRate:      0.02,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm := NewShutdownManager(log)

		// Register chaos components
		for j := 0; j < config.ComponentCount; j++ {
			comp := NewChaosComponent(
				fmt.Sprintf("bench-comp-%d", j),
				j%6,
				TimeoutTestFast,
				config,
			)

			component := comp
			sm.Register(component.Name, component.Priority, component.Timeout, func(ctx context.Context) error {
				return component.Execute(ctx)
			})
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = sm.Shutdown(ctx)
		cancel()
	}
}

// TestShutdownManager_ChaosResourceExhaustion tests behavior when system resources are exhausted
func TestShutdownManager_ChaosResourceExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos resource exhaustion test in short mode")
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	sm := NewShutdownManager(log)

	// Register components that exhaust various system resources
	sm.Register("goroutine-bomb", PriorityHigh, TimeoutStandard, func(ctx context.Context) error {
		// Create many goroutines to stress the scheduler
		var wg sync.WaitGroup
		for i := 0; i < 1000; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(10 * time.Millisecond)
			}()
		}
		wg.Wait()
		return nil
	})

	sm.Register("channel-flood", PriorityNormal, TimeoutStandard, func(ctx context.Context) error {
		// Create many channels to test channel handling
		channels := make([]chan struct{}, 1000)
		for i := range channels {
			channels[i] = make(chan struct{}, 1)
			close(channels[i])
		}
		return nil
	})

	sm.Register("timer-stress", PriorityLow, TimeoutStandard, func(ctx context.Context) error {
		// Create many timers to stress time management
		var timers []*time.Timer
		for i := 0; i < 500; i++ {
			timer := time.NewTimer(1 * time.Millisecond)
			timers = append(timers, timer)
		}

		// Clean up timers
		for _, timer := range timers {
			timer.Stop()
		}
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	err := sm.Shutdown(ctx)
	duration := time.Since(start)

	// Shutdown should handle resource exhaustion gracefully
	assert.Less(t, duration, 12*time.Second, "Shutdown should complete even with resource exhaustion")
	if err != nil {
		t.Logf("Resource exhaustion test result: %v", err)
	}
}
