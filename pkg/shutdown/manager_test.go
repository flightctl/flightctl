package shutdown

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

func TestShutdown(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Shutdown Manager Suite")
}

// mockServer simulates a server with controllable behavior
type mockServer struct {
	name     string
	runDelay time.Duration
	runError error
	runCalls int
}

func (m *mockServer) Run(ctx context.Context) error {
	m.runCalls++
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

var _ = Describe("Shutdown Manager", func() {
	var (
		log     *logrus.Logger
		manager *Manager
	)

	BeforeEach(func() {
		log, _ = test.NewNullLogger()
		manager = NewManager(log)
	})

	Context("Builder Pattern", func() {
		It("should initialize with default signals", func() {
			Expect(manager.signals).To(HaveLen(3))
			Expect(manager.signals).To(ContainElements(
				syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT,
			))
		})

		It("should add servers using builder pattern", func() {
			server1 := &mockServer{name: "server1"}
			server2 := &mockServer{name: "server2"}

			result := manager.AddServer("server1", server1).AddServer("server2", server2)

			Expect(result).To(Equal(manager)) // Returns same instance for chaining
			Expect(manager.servers).To(HaveLen(2))
			Expect(manager.servers[0].name).To(Equal("server1"))
			Expect(manager.servers[1].name).To(Equal("server2"))
		})

		It("should add cleanup functions using builder pattern", func() {
			cleanup1 := func() error { return nil }
			cleanup2 := func() error { return nil }

			result := manager.AddCleanup("cleanup1", cleanup1).AddCleanup("cleanup2", cleanup2)

			Expect(result).To(Equal(manager)) // Returns same instance for chaining
			Expect(manager.cleanups).To(HaveLen(2))
			Expect(manager.cleanups[0].name).To(Equal("cleanup1"))
			Expect(manager.cleanups[1].name).To(Equal("cleanup2"))
		})

		It("should allow custom signals", func() {
			result := manager.WithSignals(syscall.SIGTERM, syscall.SIGINT)

			Expect(result).To(Equal(manager)) // Returns same instance for chaining
			Expect(manager.signals).To(HaveLen(2))
			Expect(manager.signals).To(ContainElements(syscall.SIGTERM, syscall.SIGINT))
		})

		It("should allow setting force stop function", func() {
			forceStop := func() {}
			result := manager.WithForceStop(forceStop)

			Expect(result).To(Equal(manager)) // Returns same instance for chaining
			Expect(manager.forceStop).NotTo(BeNil())
		})
	})

	Context("Server Coordination", func() {
		It("should run all servers in parallel", func() {
			server1 := &mockServer{name: "server1", runDelay: 50 * time.Millisecond}
			server2 := &mockServer{name: "server2", runDelay: 50 * time.Millisecond}
			server3 := &mockServer{name: "server3", runDelay: 50 * time.Millisecond}

			manager.AddServer("server1", server1).
				AddServer("server2", server2).
				AddServer("server3", server3)

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			start := time.Now()
			err := manager.Run(ctx)
			duration := time.Since(start)

			Expect(err).To(BeNil())
			Expect(duration).To(BeNumerically("<", 150*time.Millisecond)) // Should run in parallel
			Expect(server1.runCalls).To(Equal(1))
			Expect(server2.runCalls).To(Equal(1))
			Expect(server3.runCalls).To(Equal(1))
		})

		It("should return error if no servers configured", func() {
			err := manager.Run(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no servers configured"))
		})

		It("should handle server errors with proper wrapping", func() {
			serverError := errors.New("connection failed")
			server := &mockServer{name: "api", runError: serverError}

			manager.AddServer("api", server)

			err := manager.Run(context.Background())

			Expect(err).To(HaveOccurred())
			var serverErr *ServerError
			Expect(errors.As(err, &serverErr)).To(BeTrue())
			Expect(serverErr.ServerName).To(Equal("api"))
			Expect(errors.Is(err, serverError)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("api server: connection failed"))
		})

		It("should not wrap context.Canceled errors", func() {
			server := &mockServer{name: "api", runError: context.Canceled}

			manager.AddServer("api", server)

			err := manager.Run(context.Background())

			Expect(err).To(BeNil()) // context.Canceled is treated as normal shutdown
		})

		It("should call force stop on first error", func() {
			forceStopCalled := false
			forceStop := func() { forceStopCalled = true }

			server := &mockServer{name: "api", runError: errors.New("bind error")}

			manager.AddServer("api", server).WithForceStop(forceStop)

			err := manager.Run(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(forceStopCalled).To(BeTrue())
		})
	})

	Context("Cleanup Coordination", func() {
		It("should execute cleanup in reverse order (LIFO)", func() {
			var executionOrder []string
			orderMutex := make(chan struct{}, 1)

			cleanup1 := func() error {
				orderMutex <- struct{}{}
				executionOrder = append(executionOrder, "cleanup1")
				<-orderMutex
				return nil
			}
			cleanup2 := func() error {
				orderMutex <- struct{}{}
				executionOrder = append(executionOrder, "cleanup2")
				<-orderMutex
				return nil
			}
			cleanup3 := func() error {
				orderMutex <- struct{}{}
				executionOrder = append(executionOrder, "cleanup3")
				<-orderMutex
				return nil
			}

			server := &mockServer{name: "test"}

			manager.AddServer("test", server).
				AddCleanup("first", cleanup1).
				AddCleanup("second", cleanup2).
				AddCleanup("third", cleanup3)

			err := manager.Run(context.Background())

			Expect(err).To(BeNil())
			Expect(executionOrder).To(Equal([]string{"cleanup3", "cleanup2", "cleanup1"}))
		})

		It("should continue cleanup even if some cleanups fail", func() {
			cleanup1Called := false
			cleanup2Called := false
			cleanup3Called := false

			cleanup1 := func() error {
				cleanup1Called = true
				return nil
			}
			cleanup2 := func() error {
				cleanup2Called = true
				return errors.New("cleanup2 failed")
			}
			cleanup3 := func() error {
				cleanup3Called = true
				return nil
			}

			server := &mockServer{name: "test"}

			manager.AddServer("test", server).
				AddCleanup("cleanup1", cleanup1).
				AddCleanup("cleanup2", cleanup2).
				AddCleanup("cleanup3", cleanup3)

			err := manager.Run(context.Background())

			Expect(err).To(BeNil())
			Expect(cleanup1Called).To(BeTrue())
			Expect(cleanup2Called).To(BeTrue())
			Expect(cleanup3Called).To(BeTrue())
		})
	})

	Context("Signal Handling", func() {
		It("should handle context cancellation like signals", func() {
			// Test context cancellation behavior instead of actual signals
			server := &mockServer{name: "test", runDelay: 200 * time.Millisecond}
			manager.AddServer("test", server)

			ctx, cancel := context.WithCancel(context.Background())

			go func() {
				time.Sleep(50 * time.Millisecond)
				cancel() // Simulate signal by canceling context
			}()

			err := manager.Run(ctx)

			Expect(err).To(BeNil()) // Should be treated as normal shutdown
		})
	})

	Context("Adapter Functions", func() {
		It("should work with ServerFunc adapter", func() {
			called := false
			serverFn := func(ctx context.Context) error {
				called = true
				return nil
			}

			server := NewServerFunc(serverFn)
			manager.AddServer("test", server)

			err := manager.Run(context.Background())

			Expect(err).To(BeNil())
			Expect(called).To(BeTrue())
		})

		It("should work with CloseErrFunc adapter", func() {
			called := false
			closeFn := func() error {
				called = true
				return nil
			}

			server := &mockServer{name: "test"}
			manager.AddServer("test", server).
				AddCleanup("test-cleanup", CloseErrFunc(closeFn))

			err := manager.Run(context.Background())

			Expect(err).To(BeNil())
			Expect(called).To(BeTrue())
		})

		It("should work with StopWaitFunc adapter", func() {
			stopCalled := false
			waitCalled := false

			stopFn := func() { stopCalled = true }
			waitFn := func() { waitCalled = true }

			server := &mockServer{name: "test"}
			manager.AddServer("test", server).
				AddCleanup("provider", StopWaitFunc("provider", stopFn, waitFn))

			err := manager.Run(context.Background())

			Expect(err).To(BeNil())
			Expect(stopCalled).To(BeTrue())
			Expect(waitCalled).To(BeTrue())
		})
	})

	Context("Error Scenarios", func() {
		It("should handle multiple server failures", func() {
			server1 := &mockServer{name: "server1", runError: errors.New("error1")}
			server2 := &mockServer{name: "server2", runError: errors.New("error2")}

			manager.AddServer("server1", server1).
				AddServer("server2", server2)

			err := manager.Run(context.Background())

			Expect(err).To(HaveOccurred())
			// Should return first error encountered
			Expect(err.Error()).To(MatchRegexp("server1 server: error1|server2 server: error2"))
		})

		It("should handle mixed success and failure", func() {
			server1 := &mockServer{name: "success", runError: nil}
			server2 := &mockServer{name: "failure", runError: errors.New("failed")}

			manager.AddServer("success", server1).
				AddServer("failure", server2)

			err := manager.Run(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failure server: failed"))
		})
	})
})
