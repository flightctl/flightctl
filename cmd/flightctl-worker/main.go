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
	"github.com/sirupsen/logrus"
)

func main() {
	log := log.InitLogs()

	if err := runCmd(log); err != nil {
		log.Fatalf("Worker service error: %v", err)
	}
}

func runCmd(log *logrus.Logger) error {
	log.Info("Starting worker service")
	defer log.Info("Worker service stopped")

	// Single context with signal handling - OS signal cancels context
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP)

	// Build cleanup functions incrementally as resources are created
	var cleanupFuncs []func() error
	defer func() {
		// First cancel context to signal all goroutines to stop
		log.Info("Cancelling context to stop all servers")
		cancel()

		// Then run cleanup in reverse order after goroutines have stopped
		log.Info("Starting cleanup")
		for i := len(cleanupFuncs) - 1; i >= 0; i-- {
			if err := cleanupFuncs[i](); err != nil {
				log.WithError(err).Error("Cleanup error")
			}
		}
		log.Info("Cleanup completed")
	}()

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		return fmt.Errorf("reading configuration: %w", err)
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-worker")
	if tracerShutdown != nil {
		cleanupFuncs = append(cleanupFuncs, func() error {
			log.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	log.Info("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	cleanupFuncs = append(cleanupFuncs, func() error {
		log.Info("Closing database connections")
		return store.Close()
	})

	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-worker")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-worker")

	processID := fmt.Sprintf("worker-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return fmt.Errorf("failed connecting to Redis queue: %w", err)
	}
	cleanupFuncs = append(cleanupFuncs, func() error {
		log.Info("Stopping queue provider")
		provider.Stop()
		provider.Wait()
		return nil
	})

	k8sClient, err := k8sclient.NewK8SClient()
	if err != nil {
		log.WithError(err).Warning("initializing k8s client, assuming k8s is not supported")
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
			workerCollector = worker.NewWorkerCollector(ctx, log, cfg, provider)
			collectors = append(collectors, workerCollector)
		}

		if cfg.Metrics.SystemCollector != nil && cfg.Metrics.SystemCollector.Enabled {
			if systemMetricsCollector = metrics.NewSystemCollector(ctx, cfg); systemMetricsCollector != nil {
				collectors = append(collectors, systemMetricsCollector)
				cleanupFuncs = append(cleanupFuncs, func() error {
					log.Info("Shutting down system metrics collector")
					return systemMetricsCollector.Shutdown()
				})
			}
		}

		if len(collectors) > 0 {
			go func() {
				log.Info("Starting metrics server")
				if err := tracing.RunMetricsServer(ctx, log, cfg.Metrics.Address, collectors...); err != nil {
					errCh <- fmt.Errorf("metrics server: %w", err)
				} else {
					errCh <- nil
				}
			}()
			metricsServerStarted = true
		}
	}

	server := workerserver.New(cfg, log, store, provider, k8sClient, workerCollector)

	go func() {
		log.Info("Starting worker server")
		if err := server.Run(ctx); err != nil {
			errCh <- fmt.Errorf("worker server: %w", err)
		} else {
			errCh <- nil
		}
	}()

	// Wait for all servers to complete before returning
	log.Info("Worker service started, waiting for shutdown signal...")
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
					log.Info("Triggering shutdown of all servers due to error")
					cancel()
					// Force provider shutdown to unblock worker server from provider.Wait()
					provider.Stop()
				}
			}
			log.WithError(err).Error("Server stopped with error")
		}
	}

	// Handle shutdown reason
	if errors.Is(firstError, context.Canceled) {
		log.Info("Servers stopped due to shutdown signal")
		return nil // Normal shutdown
	} else if firstError != nil {
		return firstError // Error shutdown
	}

	log.Info("Servers stopped normally")
	return nil // Normal completion
}
