package shutdown

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestRunMultiServer_Success(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Reduce noise

	var server1Started, server2Started atomic.Bool
	var server1Shutdown, server2Shutdown atomic.Bool

	server1 := ServerSpec{
		Name: "test-server-1",
		Runner: func(shutdownCtx context.Context) error {
			server1Started.Store(true)
			<-shutdownCtx.Done()
			server1Shutdown.Store(true)
			return nil
		},
	}

	server2 := ServerSpec{
		Name: "test-server-2",
		Runner: func(shutdownCtx context.Context) error {
			server2Started.Store(true)
			<-shutdownCtx.Done()
			server2Shutdown.Store(true)
			return nil
		},
	}

	config := NewMultiServerConfig("test service", logger)
	config.GracefulTimeout = 100 * time.Millisecond

	// Run servers in background
	done := make(chan error, 1)
	go func() {
		done <- config.RunMultiServer(context.Background(), []ServerSpec{server1, server2})
	}()

	// Wait for servers to start
	require.Eventually(t, func() bool {
		return server1Started.Load() && server2Started.Load()
	}, time.Second, 10*time.Millisecond)

	// Send shutdown signal
	time.Sleep(50 * time.Millisecond) // Let servers run briefly
	process, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)

	err = process.Signal(syscall.SIGTERM)
	require.NoError(t, err)

	// Wait for completion
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RunMultiServer did not complete within timeout")
	}

	require.True(t, server1Shutdown.Load(), "Server 1 should have received shutdown signal")
	require.True(t, server2Shutdown.Load(), "Server 2 should have received shutdown signal")
}

func TestRunMultiServer_ServerError(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Reduce noise

	expectedError := errors.New("server failed")
	var server2Completed atomic.Bool

	server1 := ServerSpec{
		Name: "failing-server",
		Runner: func(shutdownCtx context.Context) error {
			return expectedError
		},
	}

	server2 := ServerSpec{
		Name: "normal-server",
		Runner: func(shutdownCtx context.Context) error {
			// Complete normally without waiting for shutdown signal
			server2Completed.Store(true)
			return nil
		},
	}

	config := NewMultiServerConfig("test service", logger)

	err := config.RunMultiServer(context.Background(), []ServerSpec{server1, server2})
	require.Error(t, err)
	require.Contains(t, err.Error(), "server failed")
	require.True(t, server2Completed.Load(), "Second server should have completed normally")
}

func TestRunMultiServer_MetricsServerGracePeriod(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Reduce noise

	var mu sync.Mutex
	var metricsStarted, metricsShutdown, regularShutdown atomic.Bool
	var metricsShutdownTime, regularShutdownTime time.Time

	regularServer := ServerSpec{
		Name: "regular-server",
		Runner: func(shutdownCtx context.Context) error {
			<-shutdownCtx.Done()
			mu.Lock()
			regularShutdownTime = time.Now()
			mu.Unlock()
			regularShutdown.Store(true)
			return nil
		},
	}

	metricsServer := ServerSpec{
		Name:      "metrics-server",
		IsMetrics: true,
		Runner: func(shutdownCtx context.Context) error {
			metricsStarted.Store(true)
			<-shutdownCtx.Done()
			time.Sleep(50 * time.Millisecond) // Simulate metrics export
			mu.Lock()
			metricsShutdownTime = time.Now()
			mu.Unlock()
			metricsShutdown.Store(true)
			return nil
		},
	}

	config := NewMultiServerConfig("test service", logger)
	config.GracefulTimeout = 100 * time.Millisecond
	config.MetricsTimeout = 200 * time.Millisecond

	// Run servers in background
	done := make(chan error, 1)
	go func() {
		done <- config.RunMultiServer(context.Background(), []ServerSpec{regularServer, metricsServer})
	}()

	// Wait for metrics server to start
	require.Eventually(t, func() bool {
		return metricsStarted.Load()
	}, time.Second, 10*time.Millisecond)

	// Send shutdown signal
	time.Sleep(30 * time.Millisecond)
	process, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)

	err = process.Signal(syscall.SIGTERM)
	require.NoError(t, err)

	// Wait for completion
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("RunMultiServer did not complete within timeout")
	}

	require.True(t, regularShutdown.Load(), "Regular server should have shutdown")
	require.True(t, metricsShutdown.Load(), "Metrics server should have shutdown")

	// Metrics server should have had more time to shutdown
	mu.Lock()
	defer mu.Unlock()
	if !regularShutdownTime.IsZero() && !metricsShutdownTime.IsZero() {
		timeDiff := metricsShutdownTime.Sub(regularShutdownTime)
		require.True(t, timeDiff > 40*time.Millisecond, "Metrics server should have had extended grace period")
	}
}

func TestRunMultiServer_ConfigValidation(t *testing.T) {
	logger := logrus.New()
	server := ServerSpec{
		Name:   "test-server",
		Runner: func(context.Context) error { return nil },
	}

	tests := []struct {
		name    string
		config  *MultiServerConfig
		servers []ServerSpec
		errMsg  string
	}{
		{
			name:    "no servers",
			config:  &MultiServerConfig{Logger: logger},
			servers: nil,
			errMsg:  "at least one server is required",
		},
		{
			name:    "nil logger",
			config:  &MultiServerConfig{},
			servers: []ServerSpec{server},
			errMsg:  "logger is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.RunMultiServer(context.Background(), tt.servers)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestRunMultiServer_TimeoutEnforcement(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Reduce noise

	// Create servers that hang and ignore context cancellation
	hangingServer := func(shutdownCtx context.Context) error {
		<-shutdownCtx.Done() // Wait for shutdown signal
		// Simulate a hanging server that doesn't respect cancellation
		time.Sleep(200 * time.Millisecond) // Hang longer than timeout
		return nil
	}

	servers := []ServerSpec{
		{Name: "hanging-server-1", Runner: hangingServer},
		{Name: "hanging-server-2", Runner: hangingServer},
	}

	config := NewMultiServerConfig("test-service", logger)
	config.GracefulTimeout = 50 * time.Millisecond // Short timeout

	// Run servers in background
	done := make(chan error, 1)
	go func() {
		done <- config.RunMultiServer(context.Background(), servers)
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
		// Should complete within reasonable time (much less than the 200ms hang)
		require.Less(t, duration, 150*time.Millisecond)
	case <-time.After(2 * time.Second):
		t.Fatal("RunMultiServer did not complete within timeout - the timeout enforcement is not working!")
	}
}

func TestNewMultiServerConfig(t *testing.T) {
	logger := logrus.New()
	config := NewMultiServerConfig("my-service", logger)

	require.Equal(t, "my-service", config.ServiceName)
	require.Equal(t, 30*time.Second, config.GracefulTimeout)
	require.Equal(t, 60*time.Second, config.MetricsTimeout)
	require.Equal(t, logger, config.Logger)
	require.Len(t, config.Signals, 3)
	require.Contains(t, config.Signals, syscall.SIGTERM)
	require.Contains(t, config.Signals, syscall.SIGINT)
	require.Contains(t, config.Signals, syscall.SIGQUIT)
}
