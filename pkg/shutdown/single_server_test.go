package shutdown

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestRunSingleServer_Success(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(os.Stdout) // For test visibility

	var serverStarted atomic.Bool
	var shutdownReceived atomic.Bool

	runner := func(shutdownCtx context.Context) error {
		serverStarted.Store(true)

		// Simulate server running
		<-shutdownCtx.Done()
		shutdownReceived.Store(true)

		return nil
	}

	config := NewSingleServerConfig("test-service", logger)
	config.GracefulTimeout = 100 * time.Millisecond

	// Run server in background
	done := make(chan error, 1)
	go func() {
		done <- config.RunSingleServer(runner)
	}()

	// Wait for server to start
	require.Eventually(t, func() bool {
		return serverStarted.Load()
	}, time.Second, 10*time.Millisecond)

	// Send shutdown signal
	time.Sleep(50 * time.Millisecond) // Let server run briefly
	process, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)

	err = process.Signal(syscall.SIGTERM)
	require.NoError(t, err)

	// Wait for completion
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RunSingleServer did not complete within timeout")
	}

	require.True(t, shutdownReceived.Load(), "Server should have received shutdown signal")
}

func TestRunSingleServer_ServerError(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Reduce noise

	expectedError := errors.New("server failed")
	runner := func(shutdownCtx context.Context) error {
		return expectedError
	}

	config := NewSingleServerConfig("test-service", logger)

	err := config.RunSingleServer(runner)
	require.Error(t, err)
	require.Contains(t, err.Error(), "test-service server")
	require.Contains(t, err.Error(), "server failed")
}

func TestRunSingleServer_ConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config *SingleServerConfig
		runner ServerRunner
		errMsg string
	}{
		{
			name:   "nil runner",
			config: &SingleServerConfig{Logger: logrus.New()},
			runner: nil,
			errMsg: "server runner is required",
		},
		{
			name:   "nil logger",
			config: &SingleServerConfig{},
			runner: func(context.Context) error { return nil },
			errMsg: "logger is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.RunSingleServer(tt.runner)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestRunSingleServer_ForceShutdown(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Reduce noise

	var shutdownReceived atomic.Bool

	runner := func(shutdownCtx context.Context) error {
		<-shutdownCtx.Done()
		shutdownReceived.Store(true)

		// Simulate server that takes some time but completes within grace period
		time.Sleep(30 * time.Millisecond) // Less than the 50ms timeout

		return context.Canceled
	}

	config := NewSingleServerConfig("test-service", logger)
	config.GracefulTimeout = 50 * time.Millisecond

	// Run server in background
	done := make(chan error, 1)
	go func() {
		done <- config.RunSingleServer(runner)
	}()

	// Send shutdown signal
	time.Sleep(10 * time.Millisecond)
	process, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)

	err = process.Signal(syscall.SIGTERM)
	require.NoError(t, err)

	// Wait for completion
	select {
	case err := <-done:
		require.NoError(t, err) // context.Canceled is ignored
	case <-time.After(2 * time.Second):
		t.Fatal("RunSingleServer did not complete within timeout")
	}

	require.True(t, shutdownReceived.Load(), "Server should have received shutdown signal")
}

func TestRunSingleServer_TimeoutEnforcement(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Reduce noise

	// Create a server that hangs and ignores context cancellation
	runner := func(shutdownCtx context.Context) error {
		<-shutdownCtx.Done() // Wait for shutdown signal
		// Simulate a hanging server that doesn't respect cancellation
		time.Sleep(200 * time.Millisecond) // Hang longer than timeout
		return nil
	}

	config := NewSingleServerConfig("test-service", logger)
	config.GracefulTimeout = 50 * time.Millisecond // Short timeout

	// Run server in background
	done := make(chan error, 1)
	go func() {
		done <- config.RunSingleServer(runner)
	}()

	// Send shutdown signal after brief delay
	time.Sleep(10 * time.Millisecond)
	process, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)

	err = process.Signal(syscall.SIGTERM)
	require.NoError(t, err)

	// Wait for completion
	start := time.Now()
	select {
	case err := <-done:
		duration := time.Since(start)
		// Should timeout and return an error
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to stop within")
		require.Contains(t, err.Error(), "50ms timeout")
		// Should complete relatively quickly (much less than the 200ms hang)
		require.Less(t, duration, 250*time.Millisecond)
	case <-time.After(2 * time.Second):
		t.Fatal("RunSingleServer did not complete within timeout - the timeout enforcement is not working!")
	}
}

func TestNewSingleServerConfig(t *testing.T) {
	logger := logrus.New()
	config := NewSingleServerConfig("my-service", logger)

	require.Equal(t, "my-service", config.ServiceName)
	require.Equal(t, 30*time.Second, config.GracefulTimeout)
	require.Equal(t, logger, config.Logger)
	require.Len(t, config.Signals, 3)
	require.Contains(t, config.Signals, syscall.SIGTERM)
	require.Contains(t, config.Signals, syscall.SIGINT)
	require.Contains(t, config.Signals, syscall.SIGQUIT)
}

func TestSetTimeouts(t *testing.T) {
	logger := logrus.New()

	// Test original defaults
	config1 := NewSingleServerConfig("test-service", logger)
	require.Equal(t, 30*time.Second, config1.GracefulTimeout)

	multiConfig1 := NewMultiServerConfig("test-service", logger)
	require.Equal(t, 30*time.Second, multiConfig1.GracefulTimeout)
	require.Equal(t, 60*time.Second, multiConfig1.MetricsTimeout)

	// Test single server timeout override (metricsTimeout ignored)
	gracefulTimeout := 10 * time.Millisecond
	config2 := NewSingleServerConfig("test-service", logger).SetTimeouts(&gracefulTimeout, nil)
	require.Equal(t, 10*time.Millisecond, config2.GracefulTimeout)

	// Test multi server timeout overrides
	metricsTimeout := 20 * time.Millisecond
	multiConfig2 := NewMultiServerConfig("test-service", logger).SetTimeouts(&gracefulTimeout, &metricsTimeout)
	require.Equal(t, 10*time.Millisecond, multiConfig2.GracefulTimeout)
	require.Equal(t, 20*time.Millisecond, multiConfig2.MetricsTimeout)

	// Test partial override (only graceful timeout)
	newGracefulTimeout := 15 * time.Millisecond
	multiConfig3 := NewMultiServerConfig("test-service", logger).SetTimeouts(&newGracefulTimeout, nil)
	require.Equal(t, 15*time.Millisecond, multiConfig3.GracefulTimeout)
	require.Equal(t, 60*time.Second, multiConfig3.MetricsTimeout) // Should remain default

	// Test partial override (only metrics timeout)
	newMetricsTimeout := 25 * time.Millisecond
	multiConfig4 := NewMultiServerConfig("test-service", logger).SetTimeouts(nil, &newMetricsTimeout)
	require.Equal(t, 30*time.Second, multiConfig4.GracefulTimeout) // Should remain default
	require.Equal(t, 25*time.Millisecond, multiConfig4.MetricsTimeout)

	// Test that new configs still have defaults
	config3 := NewSingleServerConfig("test-service", logger)
	require.Equal(t, 30*time.Second, config3.GracefulTimeout)

	multiConfig5 := NewMultiServerConfig("test-service", logger)
	require.Equal(t, 30*time.Second, multiConfig5.GracefulTimeout)
	require.Equal(t, 60*time.Second, multiConfig5.MetricsTimeout)
}
