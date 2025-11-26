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

func TestAPIMain(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "API Main Suite")
}

// mockServer simulates a server with controllable behavior
type mockServer struct {
	name     string
	runDelay time.Duration
	runError error
}

func (m *mockServer) Run(ctx context.Context) error {
	if m.runDelay > 0 {
		select {
		case <-ctx.Done():
			return context.Canceled
		case <-time.After(m.runDelay):
			// Continue to return error if specified
		}
	}
	return m.runError
}

// simulateServerShutdown simulates the shutdown coordination pattern from runServers
func simulateServerShutdown(servers []*mockServer) ([]error, error) {
	errCh := make(chan error, len(servers))
	var collectedErrors []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure context is always canceled

	// Start all servers
	for _, server := range servers {
		s := server
		go func(s *mockServer) {
			err := s.Run(ctx)
			if err != nil {
				errCh <- fmt.Errorf("%s: %w", s.name, err)
			} else {
				errCh <- nil
			}
		}(s)
	}

	// Wait for all servers to complete (same logic as actual runServers)
	var firstError error
	for i := 0; i < len(servers); i++ {
		if err := <-errCh; err != nil {
			if firstError == nil {
				firstError = err
				// Cancel context on first error to trigger shutdown of all other servers
				if !errors.Is(err, context.Canceled) {
					cancel()
				}
			}
			collectedErrors = append(collectedErrors, err)
		}
	}

	return collectedErrors, firstError
}

var _ = Describe("API Server Shutdown Coordination", func() {
	Context("when all servers complete successfully", func() {
		It("should return no error", func() {
			servers := []*mockServer{
				{name: "API server", runError: nil},
				{name: "agent server", runError: nil},
				{name: "metrics server", runError: nil},
			}

			collectedErrors, firstError := simulateServerShutdown(servers)

			Expect(firstError).To(BeNil())
			Expect(collectedErrors).To(BeEmpty())
		})
	})

	Context("when all servers are canceled", func() {
		It("should detect context cancellation for normal shutdown", func() {
			servers := []*mockServer{
				{name: "API server", runError: context.Canceled},
				{name: "agent server", runError: context.Canceled},
				{name: "metrics server", runError: context.Canceled},
			}

			collectedErrors, firstError := simulateServerShutdown(servers)

			Expect(firstError).NotTo(BeNil())
			Expect(errors.Is(firstError, context.Canceled)).To(BeTrue())
			Expect(collectedErrors).To(HaveLen(3))

			// Verify all errors are wrapped with server names (order may vary)
			errorMessages := make([]string, len(collectedErrors))
			for i, err := range collectedErrors {
				errorMessages[i] = err.Error()
			}
			Expect(errorMessages).To(ContainElement(ContainSubstring("API server:")))
			Expect(errorMessages).To(ContainElement(ContainSubstring("agent server:")))
			Expect(errorMessages).To(ContainElement(ContainSubstring("metrics server:")))
		})
	})

	Context("when one server fails with real error", func() {
		It("should return the first error and log all server errors", func() {
			servers := []*mockServer{
				{name: "API server", runError: errors.New("listen tcp :8080: bind: address already in use")},
				{name: "agent server", runError: context.Canceled},
				{name: "metrics server", runError: context.Canceled},
			}

			collectedErrors, firstError := simulateServerShutdown(servers)

			Expect(firstError).NotTo(BeNil())
			Expect(collectedErrors).To(HaveLen(3))

			// Verify that one of the errors contains the API server error
			errorMessages := make([]string, len(collectedErrors))
			for i, err := range collectedErrors {
				errorMessages[i] = err.Error()
			}
			Expect(errorMessages).To(ContainElement(And(
				ContainSubstring("API server:"),
				ContainSubstring("address already in use"),
			)))
			Expect(errorMessages).To(ContainElement(ContainSubstring("agent server:")))
			Expect(errorMessages).To(ContainElement(ContainSubstring("metrics server:")))
		})
	})

	Context("when multiple servers fail", func() {
		It("should preserve first error and trigger cancellation of others", func() {
			servers := []*mockServer{
				{name: "API server", runDelay: 50 * time.Millisecond, runError: errors.New("first error")},
				{name: "agent server", runDelay: 100 * time.Millisecond, runError: errors.New("second error")},
				{name: "metrics server", runDelay: 25 * time.Millisecond, runError: errors.New("earliest error")},
			}

			collectedErrors, firstError := simulateServerShutdown(servers)

			Expect(firstError).NotTo(BeNil())
			Expect(collectedErrors).To(HaveLen(3))

			// Due to context cancellation on first error, slower servers will be canceled
			// The first error should be from the fastest failing server (metrics with 25ms delay)
			errorMessages := make([]string, len(collectedErrors))
			for i, err := range collectedErrors {
				errorMessages[i] = err.Error()
			}

			// The earliest error (metrics server) should be first
			Expect(errorMessages).To(ContainElement(ContainSubstring("metrics server: earliest error")))

			// Other servers should be cancelled after the first error
			Expect(errorMessages).To(ContainElement(ContainSubstring("context canceled")))
		})
	})

	Context("when mixed success and failure", func() {
		It("should only collect errors from failed servers", func() {
			servers := []*mockServer{
				{name: "API server", runError: nil}, // success
				{name: "agent server", runError: errors.New("agent failed")},
				{name: "metrics server", runError: nil}, // success
			}

			collectedErrors, firstError := simulateServerShutdown(servers)

			Expect(firstError).NotTo(BeNil())
			Expect(firstError.Error()).To(ContainSubstring("agent server: agent failed"))
			Expect(collectedErrors).To(HaveLen(1))
			Expect(collectedErrors[0].Error()).To(ContainSubstring("agent server: agent failed"))
		})
	})

	Context("context cancellation behavior", func() {
		It("should trigger context cancellation when first server fails with real error", func() {
			// Test the context cancellation scenario directly
			errCh := make(chan error, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel() // Ensure context is always canceled

			// Start first server that fails immediately
			go func() {
				errCh <- fmt.Errorf("metrics server: %w", errors.New("port already in use"))
			}()

			// Start second server that waits for context cancellation
			go func() {
				select {
				case <-ctx.Done():
					errCh <- fmt.Errorf("API server: %w", context.Canceled)
				case <-time.After(200 * time.Millisecond):
					errCh <- fmt.Errorf("API server: %w", errors.New("server should have been canceled"))
				}
			}()

			// Simulate the server coordination logic
			var collectedErrors []error
			var firstError error
			serversStarted := 2

			for i := 0; i < serversStarted; i++ {
				if err := <-errCh; err != nil {
					if firstError == nil {
						firstError = err
						// Cancel context on first error to trigger shutdown of all other servers
						if !errors.Is(err, context.Canceled) {
							cancel()
						}
					}
					collectedErrors = append(collectedErrors, err)
				}
			}

			Expect(firstError).NotTo(BeNil())
			Expect(collectedErrors).To(HaveLen(2))

			errorMessages := make([]string, len(collectedErrors))
			for i, err := range collectedErrors {
				errorMessages[i] = err.Error()
			}

			// Verify both servers failed as expected
			Expect(errorMessages).To(ContainElement(ContainSubstring("metrics server: port already in use")))
			Expect(errorMessages).To(ContainElement(ContainSubstring("API server: context canceled")))
		})
	})

	Context("error wrapping behavior", func() {
		It("should maintain server identification in wrapped errors", func() {
			originalError := errors.New("connection refused")
			wrappedError := fmt.Errorf("API server: %w", originalError)

			// Verify the wrapped error contains server name
			Expect(wrappedError.Error()).To(Equal("API server: connection refused"))

			// Verify we can still detect the underlying error
			Expect(errors.Is(wrappedError, originalError)).To(BeTrue())
		})

		It("should maintain context.Canceled detection through wrapping", func() {
			wrappedCanceled := fmt.Errorf("metrics server: %w", context.Canceled)

			Expect(errors.Is(wrappedCanceled, context.Canceled)).To(BeTrue())
			Expect(wrappedCanceled.Error()).To(Equal("metrics server: context canceled"))
		})
	})
})
