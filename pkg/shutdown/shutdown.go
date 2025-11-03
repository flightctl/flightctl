package shutdown

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	DefaultGracefulShutdownTimeout = 30 * time.Second
)

// defaultShutdownFunc is the default no-op shutdown function used when nil is passed
var defaultShutdownFunc = func(context.Context) error { return nil }

// GracefulShutdown sets up graceful shutdown handling for a service.
// It listens for SIGINT, SIGTERM, and SIGQUIT signals and calls the provided shutdown function.
// The shutdownComplete channel is closed when shutdown signal is received.
// If shutdownFunc is nil, it uses default behavior (just closes the channel).
// The shutdown function receives a context with timeout and must respect context cancellation
// to ensure timely completion. If the shutdown function returns an error, it is logged but
// the channel is still closed and process exit handling is left to the caller.
func GracefulShutdown(log *logrus.Logger, shutdownComplete chan struct{}, shutdownFunc func(context.Context) error) {
	GracefulShutdownWithTimeout(log, shutdownComplete, shutdownFunc, DefaultGracefulShutdownTimeout)
}

// GracefulShutdownWithTimeout sets up graceful shutdown handling with a custom timeout.
func GracefulShutdownWithTimeout(log *logrus.Logger, shutdownComplete chan struct{}, shutdownFunc func(context.Context) error, timeout time.Duration) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	go func() {
		sig := <-signals
		log.Infof("Received signal %s, initiating graceful shutdown", sig.String())

		// If no custom shutdown function provided, use default behavior
		if shutdownFunc == nil {
			shutdownFunc = defaultShutdownFunc
		}

		log.Info("Graceful shutdown signal received")

		// Create timeout context for cleanup
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		// Execute shutdown function
		if err := shutdownFunc(ctx); err != nil {
			log.WithError(err).Error("Graceful shutdown function failed")
		}

		// Signal shutdown completion
		close(shutdownComplete)
	}()
}
