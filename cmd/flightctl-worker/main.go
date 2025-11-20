package main

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	workerserver "github.com/flightctl/flightctl/internal/worker_server"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("reading configuration")
	}

	if err := runCmd(cfg); err != nil {
		log.InitLogs().WithError(err).Fatal("Worker service error")
	}
}

func runCmd(cfg *config.Config) error {
	logger := log.InitLogs(cfg.Service.LogLevel)

	logger.Infof("Using config: %s", cfg)
	logger.Info("Starting worker service")
	defer logger.Info("Worker service stopped")

	// Single context with signal handling - OS signal cancels context
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP)

	// Build cleanup functions incrementally as resources are created
	var cleanupFuncs []func() error
	defer func() {
		// First cancel context to signal all goroutines to stop
		logger.Info("Cancelling context to stop all servers")
		cancel()

		// Then run cleanup in reverse order after goroutines have stopped
		logger.Info("Starting cleanup")
		for i := len(cleanupFuncs) - 1; i >= 0; i-- {
			if err := cleanupFuncs[i](); err != nil {
				logger.WithError(err).Error("Cleanup error")
			}
		}
		logger.Info("Cleanup completed")
	}()

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-worker")
	if tracerShutdown != nil {
		cleanupFuncs = append(cleanupFuncs, func() error {
			logger.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	logger.Info("Initializing data store")
	db, err := store.InitDB(cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, logger.WithField("pkg", "store"))
	cleanupFuncs = append(cleanupFuncs, func() error {
		logger.Info("Closing database connections")
		return store.Close()
	})

	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-worker")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-worker")

	processID := fmt.Sprintf("worker-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, logger, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return fmt.Errorf("failed connecting to Redis queue: %w", err)
	}
	cleanupFuncs = append(cleanupFuncs, func() error {
		logger.Info("Stopping queue provider")
		provider.Stop()
		provider.Wait()
		return nil
	})

	k8sClient, err := k8sclient.NewK8SClient()
	if err != nil {
		logger.WithError(err).Warning("initializing k8s client, assuming k8s is not supported")
		k8sClient = nil
	}

	// Initialize metrics collectors
	var workerCollector *worker.WorkerCollector
	var systemMetricsCollector *metrics.SystemCollector
	errCh := make(chan error, 2)
	metricsServerStarted := false

	if cfg.Metrics != nil && cfg.Metrics.Enabled {
		var collectors []prometheus.Collector
		if cfg.Metrics.WorkerCollector != nil && cfg.Metrics.WorkerCollector.Enabled {
			workerCollector = worker.NewWorkerCollector(ctx, logger, cfg, provider)
			collectors = append(collectors, workerCollector)
		}

		if cfg.Metrics.SystemCollector != nil && cfg.Metrics.SystemCollector.Enabled {
			if systemMetricsCollector = metrics.NewSystemCollector(ctx, cfg); systemMetricsCollector != nil {
				collectors = append(collectors, systemMetricsCollector)
				cleanupFuncs = append(cleanupFuncs, func() error {
					logger.Info("Shutting down system metrics collector")
					return systemMetricsCollector.Shutdown()
				})
			}
		}

		if len(collectors) > 0 {
			go func() {
				logger.Info("Starting metrics server")
				if err := tracing.RunMetricsServer(ctx, logger, cfg.Metrics.Address, collectors...); err != nil {
					errCh <- fmt.Errorf("metrics server: %w", err)
				} else {
					errCh <- nil
				}
			}()
			metricsServerStarted = true
		}
	}

	server := workerserver.New(cfg, logger, store, provider, k8sClient, workerCollector)

	go func() {
		logger.Info("Starting worker server")
		if err := server.Run(ctx); err != nil {
			errCh <- fmt.Errorf("worker server: %w", err)
		} else {
			errCh <- nil
		}
	}()

	// Wait for all servers to complete before returning
	logger.Info("Worker service started, waiting for shutdown signal...")
	serversStarted := 1 // Always start worker server
	if metricsServerStarted {
		serversStarted++ // Metrics server was started
	}

	var firstError error
	for i := 0; i < serversStarted; i++ {
		if err := <-errCh; err != nil {
			if firstError == nil {
				firstError = err
				// Cancel context on first error to trigger shutdown of all other servers
				if !errors.Is(err, context.Canceled) {
					logger.Info("Triggering shutdown of all servers due to error")
					cancel()
					// Force provider shutdown to unblock worker server from provider.Wait()
					provider.Stop()
				}
			}
			logger.WithError(err).Error("Server stopped with error")
		}
	}

	// Handle shutdown reason
	if errors.Is(firstError, context.Canceled) {
		logger.Info("Servers stopped due to shutdown signal")
		return nil // Normal shutdown
	} else if firstError != nil {
		return firstError // Error shutdown
	}

	logger.Info("Servers stopped normally")
	return nil // Normal completion
}
