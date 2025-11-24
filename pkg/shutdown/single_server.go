// Package shutdown provides utilities for implementing graceful two-phase shutdown patterns
// for single and multi-server applications.
//
// Two-Phase Shutdown Explained:
//
// Traditional shutdown implementations often force immediate termination when receiving
// signals, which can lead to incomplete requests, corrupted data, or resource leaks.
// This package implements a more graceful two-phase shutdown approach:
//
// Phase 1 - Graceful Initiation:
// When a shutdown signal (SIGTERM, SIGINT, SIGQUIT) is received, the system immediately:
// - Signals all servers to stop accepting NEW requests/connections
// - Allows existing requests to complete naturally
// - HTTP servers call Shutdown() to close listeners but finish in-flight requests
// - Background workers finish current tasks but don't start new ones
//
// Phase 2 - Hard Deadline:
// If servers don't complete gracefully within the configured timeout (default 30s):
// - Enforces a hard deadline by returning an error when shutdown times out
// - Enables callers to terminate the process or take other recovery actions
// - This ensures the application doesn't hang indefinitely waiting for shutdown
//
// Benefits of Two-Phase Shutdown:
// - Prevents data corruption by allowing transactions to complete
// - Improves user experience by avoiding connection drops mid-request
// - Enables proper cleanup of resources (database connections, file handles, etc.)
// - Allows metrics to be exported during shutdown for observability
// - Provides predictable shutdown behavior with configurable timeouts
//
// Single Server Usage:
//
//	config := shutdown.NewSingleServerConfig("my-service", logger)
//	return config.RunSingleServer(func(ctx context.Context) error {
//	    return server.Run(ctx)  // Server should respect context cancellation
//	})
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

// ServerRunner is a function that runs a server and returns an error when it stops
type ServerRunner func(shutdownCtx context.Context) error

// SingleServerConfig configures the single server shutdown behavior
type SingleServerConfig struct {
	// ServiceName is used in log messages (e.g., "alert exporter", "periodic service")
	ServiceName string

	// StartMessage is logged when the server starts (optional, defaults to "Starting {ServiceName} server")
	StartMessage string

	// GracefulTimeout is how long to wait before forcing shutdown (default: 30s)
	GracefulTimeout time.Duration

	// Signals to listen for (default: SIGTERM, SIGINT, SIGQUIT)
	Signals []os.Signal

	// Logger for shutdown messages
	Logger logrus.FieldLogger
}

// NewSingleServerConfig returns a config with sensible defaults
func NewSingleServerConfig(serviceName string, logger logrus.FieldLogger) *SingleServerConfig {
	return &SingleServerConfig{
		ServiceName:     serviceName,
		GracefulTimeout: 30 * time.Second,
		Signals:         []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT},
		Logger:          logger,
	}
}

// SetTimeouts overrides shutdown timeouts
// Pass nil for any timeout you don't want to change
func (config *SingleServerConfig) SetTimeouts(gracefulTimeout, metricsTimeout *time.Duration) *SingleServerConfig {
	if gracefulTimeout != nil {
		config.GracefulTimeout = *gracefulTimeout
	}
	// metricsTimeout is ignored for single server configs
	return config
}

// RunSingleServer handles the complete lifecycle of a single server with graceful shutdown
func (config *SingleServerConfig) RunSingleServer(runner ServerRunner) error {
	if runner == nil {
		return fmt.Errorf("server runner is required")
	}
	if config.Logger == nil {
		return fmt.Errorf("logger is required")
	}

	// Apply defaults
	if config.GracefulTimeout <= 0 {
		config.GracefulTimeout = 30 * time.Second
	}
	if len(config.Signals) == 0 {
		config.Signals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT}
	}

	// Setup shutdown preparation
	shutdownCtx, initiateShutdown := context.WithCancel(context.Background())
	defer initiateShutdown()

	// Handle OS signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, config.Signals...)
	defer signal.Stop(sigCh)

	// Start server in goroutine
	serverDone := make(chan error, 1)
	go func() {
		startMessage := config.StartMessage
		if startMessage == "" {
			startMessage = fmt.Sprintf("Starting %s server", config.ServiceName)
		}
		config.Logger.Info(startMessage)
		serverDone <- runner(shutdownCtx)
	}()

	// Handle shutdown signal - two-phase shutdown
	shutdownReceived := make(chan struct{})
	signalDone := make(chan struct{})
	defer close(signalDone) // Ensure signal goroutine exits when function returns
	go func() {
		select {
		case sig := <-sigCh:
			config.Logger.WithField("signal", sig.String()).Info("Shutdown signal received, initiating graceful shutdown")
			// Phase 1: Immediately signal shutdown initiation (stop accepting new requests)
			initiateShutdown()
			// Signal that shutdown was received
			close(shutdownReceived)
		case <-signalDone:
			return
		}
	}()

	// Wait for shutdown signal or server completion
	select {
	case err := <-serverDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			config.Logger.WithError(err).Errorf("%s server failed", config.ServiceName)
			return fmt.Errorf("%s server: %w", config.ServiceName, err)
		}
		config.Logger.Infof("%s stopped gracefully", config.ServiceName)
		return nil
	case <-shutdownReceived:
		// Shutdown signal received, start grace period with hard timeout
		config.Logger.Infof("Graceful shutdown initiated, waiting up to %v for completion", config.GracefulTimeout)

		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				config.Logger.WithError(err).Errorf("%s server failed during shutdown", config.ServiceName)
				return fmt.Errorf("%s server: %w", config.ServiceName, err)
			}
			config.Logger.Infof("%s stopped gracefully", config.ServiceName)
			return nil
		case <-time.After(config.GracefulTimeout):
			// Phase 2: Hard deadline - return error to enable caller termination
			config.Logger.Errorf("Graceful shutdown timeout exceeded after %v, server did not complete", config.GracefulTimeout)
			return fmt.Errorf("%s server failed to stop within %v timeout", config.ServiceName, config.GracefulTimeout)
		}
	}
}
