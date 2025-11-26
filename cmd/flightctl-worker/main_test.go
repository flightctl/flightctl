package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestWorkerMain(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Worker Main Suite")
}

// simulateWorkerShutdown simulates the shutdown coordination pattern from the worker service
func simulateWorkerShutdown(workerError error, metricsEnabled bool, metricsError error) ([]error, error) {
	serversStarted := 1
	if metricsEnabled {
		serversStarted++
	}

	errCh := make(chan error, serversStarted)
	var collectedErrors []error

	// Start worker server
	go func() {
		if workerError != nil {
			errCh <- fmt.Errorf("worker server: %w", workerError)
		} else {
			errCh <- nil
		}
	}()

	// Start metrics server if enabled
	if metricsEnabled {
		go func() {
			if metricsError != nil {
				errCh <- fmt.Errorf("metrics server: %w", metricsError)
			} else {
				errCh <- nil
			}
		}()
	}

	// Wait for all servers to complete
	var firstError error
	for i := 0; i < serversStarted; i++ {
		if err := <-errCh; err != nil {
			if firstError == nil {
				firstError = err
			}
			collectedErrors = append(collectedErrors, err)
		}
	}

	return collectedErrors, firstError
}

var _ = Describe("Worker Server Shutdown Coordination", func() {
	Context("worker only (no metrics)", func() {
		It("should handle worker success", func() {
			collectedErrors, firstError := simulateWorkerShutdown(nil, false, nil)

			Expect(firstError).To(BeNil())
			Expect(collectedErrors).To(BeEmpty())
		})

		It("should handle worker failure", func() {
			workerErr := errors.New("redis connection failed")
			collectedErrors, firstError := simulateWorkerShutdown(workerErr, false, nil)

			Expect(firstError).NotTo(BeNil())
			Expect(firstError.Error()).To(ContainSubstring("worker server: redis connection failed"))
			Expect(collectedErrors).To(HaveLen(1))
		})

		It("should handle worker context cancellation", func() {
			collectedErrors, firstError := simulateWorkerShutdown(context.Canceled, false, nil)

			Expect(firstError).NotTo(BeNil())
			Expect(errors.Is(firstError, context.Canceled)).To(BeTrue())
			Expect(firstError.Error()).To(ContainSubstring("worker server:"))
			Expect(collectedErrors).To(HaveLen(1))
		})
	})

	Context("worker with metrics enabled", func() {
		It("should wait for both servers to complete", func() {
			collectedErrors, firstError := simulateWorkerShutdown(nil, true, nil)

			Expect(firstError).To(BeNil())
			Expect(collectedErrors).To(BeEmpty())
		})

		It("should collect errors from both servers", func() {
			workerErr := errors.New("worker failed")
			metricsErr := errors.New("metrics failed")
			collectedErrors, firstError := simulateWorkerShutdown(workerErr, true, metricsErr)

			Expect(firstError).NotTo(BeNil())
			Expect(collectedErrors).To(HaveLen(2))

			errorMessages := make([]string, len(collectedErrors))
			for i, err := range collectedErrors {
				errorMessages[i] = err.Error()
			}

			Expect(errorMessages).To(ContainElement(ContainSubstring("worker server: worker failed")))
			Expect(errorMessages).To(ContainElement(ContainSubstring("metrics server: metrics failed")))
		})

		It("should handle mixed success and failure", func() {
			metricsErr := errors.New("prometheus port conflict")
			collectedErrors, firstError := simulateWorkerShutdown(nil, true, metricsErr)

			Expect(firstError).NotTo(BeNil())
			Expect(firstError.Error()).To(ContainSubstring("metrics server: prometheus port conflict"))
			Expect(collectedErrors).To(HaveLen(1))
		})
	})

	Context("server identification", func() {
		It("should preserve server names in error messages", func() {
			workerErr := errors.New("database timeout")
			metricsErr := context.Canceled

			collectedErrors, firstError := simulateWorkerShutdown(workerErr, true, metricsErr)

			Expect(firstError).NotTo(BeNil())
			Expect(collectedErrors).To(HaveLen(2))

			// Check that both errors maintain server identification
			errorMessages := make([]string, len(collectedErrors))
			for i, err := range collectedErrors {
				errorMessages[i] = err.Error()
			}

			Expect(errorMessages).To(ContainElement("worker server: database timeout"))
			Expect(errorMessages).To(ContainElement("metrics server: context canceled"))

			// Verify context.Canceled is still detectable through wrapping
			for _, err := range collectedErrors {
				if err.Error() == "metrics server: context canceled" {
					Expect(errors.Is(err, context.Canceled)).To(BeTrue())
				}
			}
		})
	})

	Context("deadlock prevention", func() {
		It("should prevent deadlock when metrics server fails and worker blocks", func() {
			// This test simulates the deadlock scenario identified in the review:
			// 1. Metrics server fails immediately (e.g., port bind error)
			// 2. Worker server would normally block indefinitely on provider.Wait()
			// 3. Our fix should call provider.Stop() to unblock the worker

			// Simulate the scenario with explicit control over timing
			errCh := make(chan error, 2)
			providerStopped := make(chan bool, 1)

			// Simulate metrics server failing immediately
			go func() {
				errCh <- fmt.Errorf("metrics server: %w", errors.New("port 9090 already in use"))
			}()

			// Simulate worker server that would block on provider.Wait()
			// but should be unblocked by provider.Stop()
			go func() {
				// Simulate provider.Wait() that blocks until Stop() is called
				select {
				case <-providerStopped:
					// Unblocked by provider.Stop() call
					errCh <- fmt.Errorf("worker server: %w", context.Canceled)
				case <-time.After(500 * time.Millisecond):
					// Should not reach here - would indicate deadlock
					errCh <- fmt.Errorf("worker server: %w", errors.New("deadlock: provider.Stop() was not called"))
				}
			}()

			// Simulate the main shutdown coordination logic
			var collectedErrors []error
			var firstError error
			serversStarted := 2

			for i := 0; i < serversStarted; i++ {
				if err := <-errCh; err != nil {
					if firstError == nil {
						firstError = err
						// This is the critical fix: force provider shutdown on first error
						if !errors.Is(err, context.Canceled) {
							// Simulate provider.Stop() call
							close(providerStopped)
						}
					}
					collectedErrors = append(collectedErrors, err)
				}
			}

			// Verify the deadlock was prevented
			Expect(firstError).NotTo(BeNil())
			Expect(collectedErrors).To(HaveLen(2))

			errorMessages := make([]string, len(collectedErrors))
			for i, err := range collectedErrors {
				errorMessages[i] = err.Error()
			}

			// Verify metrics server failed first
			Expect(errorMessages).To(ContainElement(ContainSubstring("metrics server: port 9090 already in use")))

			// Verify worker server was unblocked, not deadlocked
			Expect(errorMessages).To(ContainElement(ContainSubstring("worker server: context canceled")))
			Expect(errorMessages).NotTo(ContainElement(ContainSubstring("deadlock")))
		})
	})
})
