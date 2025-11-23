package shutdown

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

// Default timeout values for different service types
const (
	// DefaultShutdownTimeout provides time for metrics export, alert processing, telemetry flushing
	DefaultShutdownTimeout = 30 * time.Second
	// LongRunningShutdownTimeout provides time for periodic tasks and worker jobs to complete
	LongRunningShutdownTimeout = 60 * time.Second
)

// Server represents any service that can be started and stopped with context cancellation.
type Server interface {
	Run(context.Context) error
}

// CleanupFunc represents a cleanup function that may return an error.
type CleanupFunc func() error

// Manager coordinates shutdown of multiple servers and cleanup functions.
type Manager struct {
	servers         []serverEntry
	cleanups        []cleanupEntry
	signals         []os.Signal
	forceStop       func()
	shutdownTimeout time.Duration
	log             *logrus.Logger
}

type serverEntry struct {
	name   string
	server Server
}

type cleanupEntry struct {
	name    string
	cleanup CleanupFunc
}

// NewManager creates a new shutdown manager with default OS signals.
func NewManager(log *logrus.Logger) *Manager {
	return &Manager{
		servers:  make([]serverEntry, 0),
		cleanups: make([]cleanupEntry, 0),
		// syscall.SIGHUP is reserved for re-read setup process
		signals: []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT},
		log:     log,
	}
}

// AddServer adds a server to be managed during shutdown.
// Servers are started in parallel and stopped when context is cancelled.
func (m *Manager) AddServer(name string, server Server) *Manager {
	m.servers = append(m.servers, serverEntry{
		name:   name,
		server: server,
	})
	return m
}

// AddCleanup adds a cleanup function to be called during shutdown.
// Cleanup functions are called in reverse order (LIFO) after all servers stop.
func (m *Manager) AddCleanup(name string, cleanup CleanupFunc) *Manager {
	m.cleanups = append(m.cleanups, cleanupEntry{
		name:    name,
		cleanup: cleanup,
	})
	return m
}

// WithSignals overrides the default OS signals to listen for.
func (m *Manager) WithSignals(signals ...os.Signal) *Manager {
	m.signals = signals
	return m
}

// WithForceStop sets a function to call on first error to force unblock servers.
// This is used to prevent deadlock scenarios (e.g., calling provider.Stop()).
func (m *Manager) WithForceStop(forceStop func()) *Manager {
	m.forceStop = forceStop
	return m
}

// WithTimeout sets a timeout for the entire shutdown process.
// If shutdown takes longer than this timeout, the process will be terminated.
func (m *Manager) WithTimeout(timeout time.Duration) *Manager {
	m.shutdownTimeout = timeout
	return m
}

// Run starts all servers and waits for shutdown signal or server failure.
// It handles the complete lifecycle: start servers -> wait for signal/error -> cleanup.
func (m *Manager) Run(ctx context.Context) error {
	if len(m.servers) == 0 {
		return errors.New("no servers configured")
	}

	// Create context with signal handling
	ctx, cancel := signal.NotifyContext(ctx, m.signals...)

	// Add timeout if specified
	if m.shutdownTimeout > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, m.shutdownTimeout)
		// Chain the cancellations
		originalCancel := cancel
		cancel = func() {
			timeoutCancel()
			originalCancel()
		}
		m.log.Infof("Shutdown timeout set to %v", m.shutdownTimeout)
	}

	// Setup cleanup to run exactly once in reverse order
	defer func() {
		m.log.Info("Cancelling context to stop all servers")
		cancel()

		m.log.Info("Starting cleanup")
		for i := len(m.cleanups) - 1; i >= 0; i-- {
			entry := m.cleanups[i]
			m.log.Infof("Cleaning up %s", entry.name)
			if err := entry.cleanup(); err != nil {
				m.log.WithError(err).Errorf("Cleanup error for %s", entry.name)
			}
		}
		m.log.Info("Cleanup completed")
	}()

	// Start all servers using errgroup for coordinated error handling
	group, groupCtx := errgroup.WithContext(ctx)

	for _, entry := range m.servers {
		// Capture loop variable for closure
		server := entry
		group.Go(func() error {
			m.log.Infof("Starting %s server", server.name)
			if err := server.server.Run(groupCtx); err != nil {
				// Wrap error with server identification
				if errors.Is(err, context.Canceled) {
					return err // Don't wrap cancellation errors
				}
				return NewServerError(server.name, err)
			}
			return nil
		})
	}

	// Wait for all servers to complete
	m.log.Info("All servers started, waiting for shutdown signal...")
	err := group.Wait()

	// Handle shutdown reason
	if errors.Is(err, context.Canceled) {
		m.log.Info("Servers stopped due to shutdown signal")
		return nil // Normal shutdown
	} else if err != nil {
		m.log.WithError(err).Error("Server stopped with error")
		// Force stop on first error to prevent deadlocks
		if m.forceStop != nil {
			m.log.Info("Triggering force stop to prevent deadlocks")
			m.forceStop()
		}
		return err // Error shutdown
	}

	m.log.Info("Servers stopped normally")
	return nil // Normal completion
}

// ServerError wraps an error with server identification.
type ServerError struct {
	ServerName string
	Err        error
}

func NewServerError(serverName string, err error) *ServerError {
	return &ServerError{
		ServerName: serverName,
		Err:        err,
	}
}

func (e *ServerError) Error() string {
	return e.ServerName + " server: " + e.Err.Error()
}

func (e *ServerError) Unwrap() error {
	return e.Err
}

func (e *ServerError) Is(target error) bool {
	return errors.Is(e.Err, target)
}
