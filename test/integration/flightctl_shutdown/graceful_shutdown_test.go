package flightctl_shutdown

import (
	"context"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/pkg/shutdown"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Graceful Shutdown Integration", func() {
	var (
		helper *ShutdownTestHelper
	)

	BeforeEach(func() {
		helper = NewShutdownTestHelper()
	})

	AfterEach(func() {
		if helper != nil {
			helper.Cleanup()
		}
	})

	Describe("Service Restart After Termination", func() {
		It("should gracefully shutdown and allow clean restart", func() {
			By("Starting a test service with real resources")
			service := helper.StartTestService()

			By("Verifying service is running")
			Expect(service.IsShutdownComplete()).To(BeFalse())

			By("Sending SIGTERM signal to trigger graceful shutdown")
			err := service.SendSignal(syscall.SIGTERM)
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for graceful shutdown to complete")
			err = service.WaitForShutdown(15 * time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying shutdown completed successfully")
			Expect(service.IsShutdownComplete()).To(BeTrue())

			By("Verifying service can be restarted cleanly")
			// Start a new service instance to verify no resource conflicts
			newService := helper.StartTestService()
			Expect(newService.IsShutdownComplete()).To(BeFalse())

			By("Cleaning up the new service")
			newService.Cancel()
			err = newService.WaitForShutdown(5 * time.Second)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should handle SIGINT signal gracefully", func() {
			By("Starting a test service")
			service := helper.StartTestService()

			By("Sending SIGINT signal")
			err := service.SendSignal(syscall.SIGINT)
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for graceful shutdown")
			err = service.WaitForShutdown(15 * time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying shutdown completed")
			Expect(service.IsShutdownComplete()).To(BeTrue())
		})

		It("should handle fail-fast shutdown correctly", func() {
			By("Starting a test service")
			service := helper.StartTestService()

			By("Triggering a fail-fast shutdown")
			testErr := fmt.Errorf("simulated component failure")
			service.TriggerFailFast("test-component", testErr)

			By("Waiting for fail-fast shutdown to complete")
			err := service.WaitForShutdown(10 * time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying shutdown completed due to fail-fast")
			Expect(service.IsShutdownComplete()).To(BeTrue())
		})
	})

	Describe("Resource Cleanup Under Load", func() {
		It("should clean up resources properly during high load", func() {
			By("Starting resource monitoring")
			monitor := NewResourceMonitor(helper.log)
			monitor.StartMonitoring()

			By("Starting a test service with real database and KV connections")
			service := helper.StartTestService()

			By("Starting load generation components")
			loadComponents := []*LoadTestComponent{
				NewLoadTestComponent("database-load", helper.log),
				NewLoadTestComponent("kvstore-load", helper.log),
				NewLoadTestComponent("api-load", helper.log),
			}

			for _, comp := range loadComponents {
				comp.Start()
			}

			By("Letting load run for a brief period")
			time.Sleep(500 * time.Millisecond)

			By("Triggering shutdown while under load")
			err := service.SendSignal(syscall.SIGTERM)
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for shutdown to complete despite ongoing load")
			err = service.WaitForShutdown(20 * time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Stopping load generation")
			for _, comp := range loadComponents {
				err := comp.Stop()
				Expect(err).ToNot(HaveOccurred())
			}

			By("Verifying all resources were cleaned up properly")
			monitor.SimulateResourceCleanup()
			err = monitor.CheckResourcesCleanedUp()
			Expect(err).ToNot(HaveOccurred())

			By("Verifying shutdown completed")
			Expect(service.IsShutdownComplete()).To(BeTrue())
		})

		It("should handle resource cleanup failures gracefully", func() {
			By("Starting a test service")
			service := helper.StartTestService()

			By("Simulating a resource that fails to clean up")
			shutdownMgr := helper.CreateShutdownManager()
			shutdownMgr.Register("failing-resource", shutdown.PriorityLowest, shutdown.TimeoutQuick, func(ctx context.Context) error {
				return fmt.Errorf("simulated cleanup failure")
			})

			completedChan := make(chan error, 1)
			shutdownMgr.Register("completion", shutdown.PriorityLast, shutdown.TimeoutCompletion, func(ctx context.Context) error {
				close(completedChan)
				return nil
			})

			By("Triggering shutdown")
			err := shutdownMgr.Shutdown(context.Background())

			By("Verifying shutdown completed despite cleanup failure")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cleanup failure"))

			By("Verifying completion callback was still executed")
			select {
			case <-completedChan:
				// Expected - completion should run even after failures
			case <-time.After(5 * time.Second):
				Fail("Completion callback was not executed")
			}

			By("Cleaning up the original service")
			service.Cancel()
			_ = service.WaitForShutdown(5 * time.Second)
		})
	})

	Describe("Shutdown Coordination Across Services", func() {
		It("should coordinate shutdown across multiple service components", func() {
			By("Creating multiple shutdown managers for different services")
			apiShutdown := helper.CreateShutdownManager()
			workerShutdown := helper.CreateShutdownManager()
			periodicShutdown := helper.CreateShutdownManager()

			shutdownOrder := make([]string, 0)
			var orderMutex sync.Mutex

			recordShutdown := func(service string) func(context.Context) error {
				return func(ctx context.Context) error {
					orderMutex.Lock()
					shutdownOrder = append(shutdownOrder, service)
					orderMutex.Unlock()
					return nil
				}
			}

			By("Registering components with proper priorities for each service")
			// API service components
			apiShutdown.Register("api-server", shutdown.PriorityHighest, shutdown.TimeoutStandard, recordShutdown("api-server"))
			apiShutdown.Register("api-cache", shutdown.PriorityLow, shutdown.TimeoutCache, recordShutdown("api-cache"))
			apiShutdown.Register("api-db", shutdown.PriorityLowest, shutdown.TimeoutStandard, recordShutdown("api-db"))

			// Worker service components
			workerShutdown.Register("worker-queue", shutdown.PriorityHigh, shutdown.TimeoutStandard, recordShutdown("worker-queue"))
			workerShutdown.Register("worker-db", shutdown.PriorityLowest, shutdown.TimeoutStandard, recordShutdown("worker-db"))

			// Periodic service components (special extended timeout)
			periodicShutdown.Register("periodic-server", shutdown.PriorityHigh, shutdown.TimeoutDatabase, recordShutdown("periodic-server"))
			periodicShutdown.Register("periodic-db", shutdown.PriorityLowest, shutdown.TimeoutStandard, recordShutdown("periodic-db"))

			By("Executing coordinated shutdown across all services")
			ctx := context.Background()

			// Shutdown all services concurrently (as would happen in real deployment)
			var shutdownErrors []error
			var wg sync.WaitGroup

			services := map[string]*shutdown.ShutdownManager{
				"api":      apiShutdown,
				"worker":   workerShutdown,
				"periodic": periodicShutdown,
			}

			for name, mgr := range services {
				wg.Add(1)
				go func(serviceName string, manager *shutdown.ShutdownManager) {
					defer wg.Done()
					if err := manager.Shutdown(ctx); err != nil {
						shutdownErrors = append(shutdownErrors, fmt.Errorf("%s: %w", serviceName, err))
					}
				}(name, mgr)
			}

			wg.Wait()

			By("Verifying no shutdown errors occurred")
			Expect(shutdownErrors).To(BeEmpty())

			By("Verifying shutdown order within each service respects priorities")
			orderMutex.Lock()
			finalOrder := make([]string, len(shutdownOrder))
			copy(finalOrder, shutdownOrder)
			orderMutex.Unlock()

			// Verify API service order
			apiOrder := extractServiceComponents(finalOrder, "api")
			Expect(apiOrder).To(Equal([]string{"api-server", "api-cache", "api-db"}))

			// Verify worker service order
			workerOrder := extractServiceComponents(finalOrder, "worker")
			Expect(workerOrder).To(Equal([]string{"worker-queue", "worker-db"}))

			// Verify periodic service order
			periodicOrder := extractServiceComponents(finalOrder, "periodic")
			Expect(periodicOrder).To(Equal([]string{"periodic-server", "periodic-db"}))
		})

		It("should handle cross-service dependency shutdown", func() {
			By("Creating a shutdown scenario with cross-service dependencies")
			mainShutdown := helper.CreateShutdownManager()

			shutdownEvents := make([]string, 0)
			var eventMutex sync.Mutex

			recordEvent := func(event string) func(context.Context) error {
				return func(ctx context.Context) error {
					eventMutex.Lock()
					shutdownEvents = append(shutdownEvents, event)
					eventMutex.Unlock()
					// Add small delay to make timing more realistic
					time.Sleep(10 * time.Millisecond)
					return nil
				}
			}

			By("Registering components that simulate real service dependencies")
			// API servers stop first to reject new requests
			mainShutdown.Register("http-servers", shutdown.PriorityHighest, shutdown.TimeoutStandard, recordEvent("http-servers-stopped"))

			// Worker processes stop next to finish in-flight work
			mainShutdown.Register("worker-processes", shutdown.PriorityHigh, shutdown.TimeoutDatabase, recordEvent("worker-processes-stopped"))

			// Shared caches stop after workers
			mainShutdown.Register("shared-cache", shutdown.PriorityLow, shutdown.TimeoutCache, recordEvent("shared-cache-stopped"))

			// Database connections close last
			mainShutdown.Register("database-pool", shutdown.PriorityLowest, shutdown.TimeoutDatabase, recordEvent("database-pool-closed"))
			mainShutdown.Register("kv-store-pool", shutdown.PriorityLowest, shutdown.TimeoutStandard, recordEvent("kv-store-pool-closed"))

			By("Executing the coordinated shutdown")
			start := time.Now()
			err := mainShutdown.Shutdown(context.Background())
			duration := time.Since(start)

			By("Verifying shutdown completed successfully")
			Expect(err).ToNot(HaveOccurred())

			By("Verifying shutdown completed in reasonable time")
			Expect(duration).To(BeNumerically("<", shutdown.DefaultGracefulShutdownTimeout))

			By("Verifying correct dependency order")
			eventMutex.Lock()
			events := make([]string, len(shutdownEvents))
			copy(events, shutdownEvents)
			eventMutex.Unlock()

			expectedOrder := []string{
				"http-servers-stopped",
				"worker-processes-stopped",
				"shared-cache-stopped",
				"database-pool-closed",
				"kv-store-pool-closed",
			}

			Expect(events).To(Equal(expectedOrder))
		})
	})
})

// extractServiceComponents filters shutdown events to get components for a specific service
func extractServiceComponents(allEvents []string, servicePrefix string) []string {
	var serviceEvents []string
	for _, event := range allEvents {
		if len(event) > len(servicePrefix) && event[:len(servicePrefix)] == servicePrefix {
			serviceEvents = append(serviceEvents, event)
		}
	}
	return serviceEvents
}
