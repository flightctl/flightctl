package flightctl_shutdown

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// ShutdownTestHelper provides utilities for shutdown integration tests
type ShutdownTestHelper struct {
	ctx     context.Context
	log     *logrus.Logger
	cfg     *config.Config
	dbName  string
	db      *gorm.DB
	store   store.Store
	kvStore kvstore.KVStore
}

// NewShutdownTestHelper creates a new test helper
func NewShutdownTestHelper() *ShutdownTestHelper {
	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	log := flightlog.InitLogs()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

	// Setup database like other integration tests
	store, cfg, dbName, db := store.PrepareDBForUnitTests(ctx, log)

	// Setup KV store connection
	kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
	Expect(err).ToNot(HaveOccurred())

	return &ShutdownTestHelper{
		ctx:     ctx,
		log:     log,
		cfg:     cfg,
		dbName:  dbName,
		db:      db,
		store:   store,
		kvStore: kvStore,
	}
}

// Cleanup performs cleanup for the test helper
func (h *ShutdownTestHelper) Cleanup() {
	if h.kvStore != nil {
		err := h.kvStore.DeleteAllKeys(h.ctx)
		Expect(err).ToNot(HaveOccurred())
		h.kvStore.Close()
	}
	if h.store != nil {
		store.DeleteTestDB(h.ctx, h.log, h.cfg, h.store, h.dbName)
	}
}

// CreateShutdownManager creates a shutdown manager for testing
func (h *ShutdownTestHelper) CreateShutdownManager() *shutdown.ShutdownManager {
	return shutdown.NewShutdownManager(h.log)
}

// TestServiceProcess represents a service process for testing
type TestServiceProcess struct {
	shutdownMgr  *shutdown.ShutdownManager
	log          *logrus.Logger
	shutdownDone chan error
	ctx          context.Context
	cancel       context.CancelFunc
}

// StartTestService starts a test service that simulates a real FlightCtl service
func (h *ShutdownTestHelper) StartTestService() *TestServiceProcess {
	ctx, cancel := context.WithCancel(h.ctx)

	shutdownMgr := h.CreateShutdownManager()
	shutdownMgr.EnableFailFast(cancel)

	// Don't set up global signal handling in tests to avoid conflicts with test framework
	// The SendSignal method will directly call shutdown instead

	shutdownDone := make(chan error, 1)

	// Simulate service components
	h.registerTestComponents(shutdownMgr, shutdownDone)

	return &TestServiceProcess{
		shutdownMgr:  shutdownMgr,
		log:          h.log,
		shutdownDone: shutdownDone,
		ctx:          ctx,
		cancel:       cancel,
	}
}

// registerTestComponents registers components that simulate real service components
func (h *ShutdownTestHelper) registerTestComponents(shutdownMgr *shutdown.ShutdownManager, done chan error) {
	// Simulate HTTP server (highest priority)
	shutdownMgr.Register("http-server", shutdown.PriorityHighest, shutdown.TimeoutStandard, func(ctx context.Context) error {
		h.log.Info("Shutting down HTTP server")
		return nil
	})

	// Simulate caches
	shutdownMgr.Register("cache", shutdown.PriorityLow, shutdown.TimeoutCache, func(ctx context.Context) error {
		h.log.Info("Shutting down cache")
		return nil
	})

	// Simulate database cleanup (using real database connection)
	shutdownMgr.Register("database", shutdown.PriorityLowest, shutdown.TimeoutDatabase, func(ctx context.Context) error {
		h.log.Info("Closing database connections")
		// Close the real database connection to test resource cleanup
		if h.store != nil {
			h.store.Close()
		}
		return nil
	})

	// Simulate KV store cleanup (using real KV connection)
	shutdownMgr.Register("kvstore", shutdown.PriorityLowest, shutdown.TimeoutStandard, func(ctx context.Context) error {
		h.log.Info("Closing KV store connections")
		// Clean and close the real KV store to test resource cleanup
		if h.kvStore != nil {
			err := h.kvStore.DeleteAllKeys(h.ctx)
			if err != nil {
				return fmt.Errorf("failed to clean KV store: %w", err)
			}
			h.kvStore.Close()
		}
		return nil
	})

	// Completion marker
	shutdownMgr.Register("completion", shutdown.PriorityLast, shutdown.TimeoutCompletion, func(ctx context.Context) error {
		h.log.Info("Shutdown completed")
		close(done)
		return nil
	})
}

// SendSignal simulates sending a signal by directly triggering shutdown
func (p *TestServiceProcess) SendSignal(sig os.Signal) error {
	// Instead of sending real signals, directly trigger shutdown to avoid interfering with test framework
	go func() {
		defer func() {
			if r := recover(); r != nil {
				p.log.WithField("panic", r).Error("Panic during shutdown in test")
				p.shutdownDone <- fmt.Errorf("panic during shutdown: %v", r)
			}
		}()

		p.log.WithField("signal", sig.String()).Info("Simulating signal - triggering shutdown")
		err := p.shutdownMgr.Shutdown(p.ctx)

		// Only send to channel if it's not already closed
		select {
		case p.shutdownDone <- err:
		case <-time.After(100 * time.Millisecond):
			// Channel might be closed or full, that's okay
		}
	}()
	return nil
}

// WaitForShutdown waits for the service to complete shutdown
func (p *TestServiceProcess) WaitForShutdown(timeout time.Duration) error {
	select {
	case err := <-p.shutdownDone:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("shutdown timed out after %v", timeout)
	}
}

// TriggerFailFast triggers a fail-fast shutdown
func (p *TestServiceProcess) TriggerFailFast(component string, err error) {
	p.shutdownMgr.TriggerFailFast(component, err)
}

// IsShutdownComplete checks if shutdown has completed
func (p *TestServiceProcess) IsShutdownComplete() bool {
	select {
	case <-p.shutdownDone:
		return true
	default:
		return false
	}
}

// Cancel cancels the service context
func (p *TestServiceProcess) Cancel() {
	p.cancel()
}

// LoadTestComponent represents a component that generates load
type LoadTestComponent struct {
	name     string
	stopChan chan struct{}
	done     chan struct{}
	log      *logrus.Logger
}

// NewLoadTestComponent creates a new load test component
func NewLoadTestComponent(name string, log *logrus.Logger) *LoadTestComponent {
	return &LoadTestComponent{
		name:     name,
		stopChan: make(chan struct{}),
		done:     make(chan struct{}),
		log:      log,
	}
}

// Start starts generating load
func (c *LoadTestComponent) Start() {
	go func() {
		defer close(c.done)
		c.log.Infof("Starting load generation for %s", c.name)

		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-c.stopChan:
				c.log.Infof("Stopping load generation for %s", c.name)
				return
			case <-ticker.C:
				// Simulate work
				time.Sleep(1 * time.Millisecond)
			}
		}
	}()
}

// Stop stops generating load
func (c *LoadTestComponent) Stop() error {
	close(c.stopChan)

	// Wait for component to stop with timeout
	select {
	case <-c.done:
		c.log.Infof("Load component %s stopped successfully", c.name)
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("load component %s failed to stop within timeout", c.name)
	}
}

// ResourceMonitor monitors resource usage during shutdown
type ResourceMonitor struct {
	dbConnections int
	kvConnections int
	log           *logrus.Logger
}

// NewResourceMonitor creates a new resource monitor
func NewResourceMonitor(log *logrus.Logger) *ResourceMonitor {
	return &ResourceMonitor{log: log}
}

// StartMonitoring starts monitoring resources
func (m *ResourceMonitor) StartMonitoring() {
	// In a real implementation, this would monitor actual system resources
	// For testing, we'll simulate resource monitoring
	m.dbConnections = 5 // Simulate 5 DB connections
	m.kvConnections = 3 // Simulate 3 KV connections
	m.log.Info("Started resource monitoring")
}

// CheckResourcesCleanedUp verifies all resources have been cleaned up
func (m *ResourceMonitor) CheckResourcesCleanedUp() error {
	// In a real implementation, this would check actual system resources
	// For testing, we'll simulate the cleanup verification
	if m.dbConnections > 0 {
		return fmt.Errorf("database connections not cleaned up: %d remaining", m.dbConnections)
	}
	if m.kvConnections > 0 {
		return fmt.Errorf("KV connections not cleaned up: %d remaining", m.kvConnections)
	}
	m.log.Info("All resources properly cleaned up")
	return nil
}

// SimulateResourceCleanup simulates resource cleanup
func (m *ResourceMonitor) SimulateResourceCleanup() {
	m.dbConnections = 0
	m.kvConnections = 0
	m.log.Info("Simulated resource cleanup")
}
