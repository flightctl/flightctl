// Package shutdown provides utilities for implementing graceful two-phase shutdown patterns
// for single and multi-server applications.
//
// Multi-Server Coordination:
//
// The multi-server pattern extends two-phase shutdown to coordinate multiple servers
// running concurrently within the same application process. This is common in
// microservices where one process may run an API server, metrics server, and
// background workers simultaneously.
//
// Key Features for Multi-Server:
//
// 1. Coordinated Shutdown:
//   - All servers receive the same shutdown signal simultaneously
//   - Shutdown proceeds in parallel for faster termination
//   - First server error triggers coordinated cleanup for all others
//
// 2. Differentiated Grace Periods:
//   - Regular servers get standard timeout (default 30s)
//   - Metrics servers get extended timeout (default 60s) to export shutdown metrics
//   - Critical for observability during application termination
//
// 3. Error Coordination:
//   - If any server fails, all servers are signaled to shut down
//   - Background tasks continue until graceful shutdown completes
//   - First error is preserved and returned to caller
//
// 4. Provider Integration:
//   - Providers (Redis queues, database pools, etc.) continue running until shutdown
//   - Background tasks can execute properly during graceful shutdown period
//   - Provider cleanup handled via standard defer patterns in service code
//
// Multi-Server Usage:
//
//	servers := []shutdown.ServerSpec{
//	    {Name: "API server", Runner: apiServer.Run},
//	    {Name: "metrics server", IsMetrics: true, Runner: metricsRunner},
//	}
//	config := shutdown.NewMultiServerConfig("my-service", logger)
//	return config.RunMultiServer(servers)
//
// Special Considerations:
// - Metrics servers should export final metrics during extended grace period
// - HTTP servers should implement proper Shutdown() methods
// - Database transactions should check context cancellation
// - Background workers should respect context timeouts
package shutdown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

// MultiServerRunner is a function that runs a server and returns an error when it stops
type MultiServerRunner func(shutdownCtx context.Context) error

// MultiServerConfig configures the multi-server shutdown behavior
type MultiServerConfig struct {
	// ServiceName is used in log messages (e.g., "API service", "worker service")
	ServiceName string

	// GracefulTimeout is how long to wait before forcing shutdown (default: 30s)
	GracefulTimeout time.Duration

	// MetricsTimeout is how long metrics server gets for shutdown metrics export (default: 60s)
	MetricsTimeout time.Duration

	// Signals to listen for (default: SIGTERM, SIGINT, SIGQUIT)
	Signals []os.Signal

	// Logger for shutdown messages
	Logger logrus.FieldLogger

	// ProviderStopper is called to stop provider/queue on first error (optional)
	ProviderStopper func()
}

// ServerSpec defines a server to run in the multi-server coordination
type ServerSpec struct {
	Name   string
	Runner MultiServerRunner
	// IsMetrics indicates this server gets extended shutdown grace period
	IsMetrics bool
}

// NewMultiServerConfig returns a config with sensible defaults
func NewMultiServerConfig(serviceName string, logger logrus.FieldLogger) *MultiServerConfig {
	return &MultiServerConfig{
		ServiceName:     serviceName,
		GracefulTimeout: 30 * time.Second,
		MetricsTimeout:  60 * time.Second,
		Signals:         []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT},
		Logger:          logger,
	}
}

// SetTimeouts overrides shutdown timeouts
// Pass nil for any timeout you don't want to change
func (config *MultiServerConfig) SetTimeouts(gracefulTimeout, metricsTimeout *time.Duration) *MultiServerConfig {
	if gracefulTimeout != nil {
		config.GracefulTimeout = *gracefulTimeout
	}
	if metricsTimeout != nil {
		config.MetricsTimeout = *metricsTimeout
	}
	return config
}

// RunMultiServer handles the complete lifecycle of multiple servers with coordinated graceful shutdown
func (config *MultiServerConfig) RunMultiServer(baseCtx context.Context, servers []ServerSpec) error {
	if len(servers) == 0 {
		return fmt.Errorf("at least one server is required")
	}
	if config.Logger == nil {
		return fmt.Errorf("logger is required")
	}

	// Apply defaults
	if config.GracefulTimeout <= 0 {
		config.GracefulTimeout = 30 * time.Second
	}
	if config.MetricsTimeout <= 0 {
		config.MetricsTimeout = 60 * time.Second
	}
	if len(config.Signals) == 0 {
		config.Signals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT}
	}

	// Setup shutdown contexts using the base context to preserve values like InternalRequestCtxKey
	forceCtx, forceCancel := context.WithCancel(baseCtx)
	shutdownCtx, initiateShutdown := context.WithCancel(baseCtx)
	defer forceCancel()
	defer initiateShutdown()

	// Handle OS signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, config.Signals...)
	defer signal.Stop(sigCh)

	// Setup server coordination
	errCh := make(chan error, len(servers))
	serversStarted := len(servers)

	// Start all servers
	for _, server := range servers {
		if server.IsMetrics {
			config.startMetricsServer(server, shutdownCtx, forceCtx, errCh)
		} else {
			config.startRegularServer(server, shutdownCtx, errCh)
		}
	}

	// Handle shutdown signal coordination
	shutdownReceived := make(chan struct{})
	signalDone := make(chan struct{})
	defer close(signalDone) // Ensure signal goroutine exits when function returns
	go func() {
		select {
		case sig := <-sigCh:
			config.Logger.WithField("signal", sig.String()).Info("Shutdown signal received, initiating graceful shutdown")
			initiateShutdown()
			close(shutdownReceived)
		case <-signalDone:
			return
		}
	}()

	config.Logger.Infof("%s started, waiting for shutdown signal...", config.ServiceName)

	// Calculate effective timeout (consider metrics servers)
	hasMetricsServers := false
	for _, server := range servers {
		if server.IsMetrics {
			hasMetricsServers = true
			break
		}
	}

	// Main coordination logic with timeout enforcement
	return config.waitForCompletion(errCh, serversStarted, shutdownReceived, forceCancel, initiateShutdown, hasMetricsServers)
}

func (config *MultiServerConfig) startRegularServer(server ServerSpec, shutdownCtx context.Context, errCh chan<- error) {
	go func() {
		config.Logger.Infof("Starting %s", server.Name)
		if err := server.Runner(shutdownCtx); err != nil {
			errCh <- fmt.Errorf("%s: %w", server.Name, err)
		} else {
			errCh <- nil
		}
	}()
}

func (config *MultiServerConfig) startMetricsServer(server ServerSpec, shutdownCtx, forceCtx context.Context, errCh chan<- error) {
	go func() {
		config.Logger.Infof("Starting %s", server.Name)

		// Create extended context for metrics server
		metricsCtx, metricsCancel := context.WithCancel(context.Background())
		defer metricsCancel()

		// Start metrics server
		metricsResult := make(chan error, 1)
		go func() {
			if err := server.Runner(metricsCtx); err != nil {
				metricsResult <- fmt.Errorf("%s: %w", server.Name, err)
			} else {
				metricsResult <- nil
			}
		}()

		// Handle graceful shutdown with extended grace period for metrics
		select {
		case err := <-metricsResult:
			errCh <- err
		case <-shutdownCtx.Done():
			config.Logger.Infof("%s received shutdown signal, starting graceful shutdown (%v grace period)",
				server.Name, config.MetricsTimeout)

			// Give metrics server extended time to export shutdown metrics
			shutdownTimer := time.NewTimer(config.MetricsTimeout)
			defer shutdownTimer.Stop()

			select {
			case err := <-metricsResult:
				config.Logger.Infof("%s completed gracefully", server.Name)
				errCh <- err
			case <-shutdownTimer.C:
				config.Logger.Warnf("%s grace period exceeded, forcing shutdown", server.Name)
				metricsCancel()
				errCh <- <-metricsResult
			case <-forceCtx.Done():
				config.Logger.Warnf("Force shutdown - stopping %s immediately", server.Name)
				metricsCancel()
				errCh <- <-metricsResult
			}
		}
	}()
}

// waitForCompletion handles server completion with real timeout enforcement
func (config *MultiServerConfig) waitForCompletion(errCh <-chan error, serversStarted int, shutdownReceived <-chan struct{}, forceCancel context.CancelFunc, initiateShutdown context.CancelFunc, hasMetricsServers bool) error {
	// First, wait for either servers to complete or shutdown signal
	completedCount := 0
	var firstError error

	for {
		select {
		case err := <-errCh:
			completedCount++

			if err != nil && !errors.Is(err, context.Canceled) && firstError == nil {
				firstError = err
				// Log the first error but don't trigger immediate shutdown
				// Let servers complete naturally unless explicitly signaled
				config.Logger.WithError(err).Warn("Server encountered error, continuing with remaining servers")
			}

			// If all servers completed, return result
			if completedCount >= serversStarted {
				if firstError != nil {
					config.Logger.WithError(firstError).Error("Server failed")
					return firstError
				}
				config.Logger.Infof("All %s servers stopped gracefully", config.ServiceName)
				return nil
			}

		case <-shutdownReceived:
			if hasMetricsServers {
				// Metrics servers handle their own timeouts (60s), but we still need a safety net
				// to prevent indefinite hangs if regular servers don't terminate.
				// Add a small buffer beyond metrics timeout to avoid competing with internal logic
				effectiveTimeout := config.MetricsTimeout + 100*time.Millisecond
				if config.GracefulTimeout > effectiveTimeout {
					effectiveTimeout = config.GracefulTimeout
				}
				config.Logger.Infof("Graceful shutdown initiated, waiting up to %v for %d remaining servers", effectiveTimeout, serversStarted-completedCount)
				return config.waitForCompletionWithTimeout(errCh, serversStarted, completedCount, firstError, forceCancel, effectiveTimeout)
			} else {
				config.Logger.Infof("Graceful shutdown initiated, waiting up to %v for %d remaining servers", config.GracefulTimeout, serversStarted-completedCount)
				return config.waitForCompletionWithTimeout(errCh, serversStarted, completedCount, firstError, forceCancel, config.GracefulTimeout)
			}
		}
	}
}

// waitForCompletionWithTimeout waits for remaining servers with hard timeout enforcement
func (config *MultiServerConfig) waitForCompletionWithTimeout(errCh <-chan error, serversStarted, completedCount int, firstError error, forceCancel context.CancelFunc, effectiveTimeout time.Duration) error {
	timeoutCh := time.After(effectiveTimeout)

	for completedCount < serversStarted {
		select {
		case err := <-errCh:
			completedCount++

			if err != nil && !errors.Is(err, context.Canceled) && firstError == nil {
				firstError = err
				// Log the first error but continue processing
				config.Logger.WithError(err).Warn("Server encountered error during shutdown, continuing")
			}

		case <-timeoutCh:
			// Phase 2: Hard timeout - force cancellation and return error immediately
			remaining := serversStarted - completedCount
			config.Logger.Errorf("Graceful shutdown timeout exceeded after %v, forcing %d remaining servers", effectiveTimeout, remaining)

			// Force cancel metrics servers
			forceCancel()

			// Return timeout error, preserving context about the first error if present
			if firstError != nil {
				return fmt.Errorf("%s: %d servers failed to stop within %v timeout (first error: %w)", config.ServiceName, remaining, effectiveTimeout, firstError)
			}
			return fmt.Errorf("%s: %d servers failed to stop within %v timeout", config.ServiceName, remaining, effectiveTimeout)
		}
	}

	if firstError != nil {
		config.Logger.WithError(firstError).Error("Server failed during shutdown")
		return firstError
	}

	config.Logger.Infof("All %s servers stopped gracefully", config.ServiceName)
	return nil
}
