package shutdown

import (
	"context"
)

// ServerFunc is an adapter to allow ordinary functions to be used as Servers.
type ServerFunc func(context.Context) error

func (f ServerFunc) Run(ctx context.Context) error {
	return f(ctx)
}

// NewServerFunc creates a Server from a function.
func NewServerFunc(fn func(context.Context) error) Server {
	return ServerFunc(fn)
}

// MetricsServerFunc creates a Server adapter for metrics servers.
// It handles the common pattern of starting metrics with collectors.
func MetricsServerFunc(runMetrics func(context.Context) error) Server {
	return ServerFunc(runMetrics)
}

// HTTPServerFunc creates a Server adapter for HTTP servers.
func HTTPServerFunc(runServer func(context.Context) error) Server {
	return ServerFunc(runServer)
}

// NoOpCleanup returns a cleanup function that does nothing but can be used as a placeholder.
func NoOpCleanup() CleanupFunc {
	return func() error { return nil }
}

// CloseFunc creates a cleanup function from a close method.
func CloseFunc(closeFn func()) CleanupFunc {
	return func() error {
		closeFn()
		return nil
	}
}

// CloseErrFunc creates a cleanup function from a close method that returns an error.
func CloseErrFunc(closeFn func() error) CleanupFunc {
	return closeFn
}

// DatabaseCloseFunc creates a cleanup function for database connections with proper logging.
func DatabaseCloseFunc(log interface{ Info(args ...interface{}) }, closeFn func() error) CleanupFunc {
	return func() error {
		log.Info("Closing database connections")
		return closeFn()
	}
}

// StopWaitFunc creates a cleanup function for providers with Stop() and Wait() methods.
func StopWaitFunc(name string, stopFn, waitFn func()) CleanupFunc {
	return func() error {
		stopFn()
		waitFn()
		return nil
	}
}
