package main

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/flightctl/flightctl/pkg/shutdown"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		return err
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-worker")

	log.Info("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return err
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))

	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-worker")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-worker")

	processID := fmt.Sprintf("worker-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return err
	}

	k8sClient, err := k8sclient.NewK8SClient()
	if err != nil {
		log.WithError(err).Warning("initializing k8s client, assuming k8s is not supported")
		k8sClient = nil
	}

	// Initialize metrics collectors
	var workerCollector *worker.WorkerCollector
	if cfg.Metrics != nil && cfg.Metrics.Enabled {
		var collectors []prometheus.Collector
		if cfg.Metrics.WorkerCollector != nil && cfg.Metrics.WorkerCollector.Enabled {
			workerCollector = worker.NewWorkerCollector(ctx, log, cfg, provider)
			collectors = append(collectors, workerCollector)
		}

		if cfg.Metrics.SystemCollector != nil && cfg.Metrics.SystemCollector.Enabled {
			if systemMetricsCollector := metrics.NewSystemCollector(ctx, cfg); systemMetricsCollector != nil {
				collectors = append(collectors, systemMetricsCollector)
			}
		}

		if len(collectors) > 0 {
			// Start metrics server in background
			go func() {
				log.Info("Starting metrics server")
				metricsServer := metrics.NewMetricsServer(log, collectors...)
				err := metricsServer.Run(ctx)

				// Always signal main thread
				select {
				case errCh <- err:
				default:
				}

				// Always cancel unless it was already a context cancellation
				if !errors.Is(err, context.Canceled) {
					cancel() // Cancel for both error AND success cases
				}

				// Log errors for debugging
				if err != nil && !errors.Is(err, context.Canceled) {
					log.Errorf("Metrics server error: %v", err)
				}
			}()
		}
	}

	server := workerserver.New(cfg, log, store, provider, k8sClient, workerCollector)

	// Start worker server
	go func() {
		log.Info("Starting worker server")
		err := server.Run(ctx)

		// Always signal main thread
		select {
		case errCh <- err:
		default:
		}

		// Always cancel unless it was already a context cancellation
		if !errors.Is(err, context.Canceled) {
			cancel() // Cancel for both error AND success cases
		}

		// Log errors for debugging
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("Worker server error: %v", err)
		}
	}()

	// Channel to coordinate shutdown completion
	shutdownComplete := make(chan struct{})

	// Cleanup function
	cleanup := func(cleanupCtx context.Context) error {
		log.Info("Starting cleanup...")

		// Cancel ctx so any ctx-aware goroutines can react
		cancel()

		// Stop queue provider
		log.Info("Stopping queue provider")
		provider.Stop()
		provider.Wait()

		// Close database
		log.Info("Closing database connections")
		store.Close()

		// Shutdown tracer
		if tracerShutdown != nil {
			log.Info("Shutting down tracer")
			if err := tracerShutdown(cleanupCtx); err != nil {
				log.WithError(err).Error("Failed to shutdown tracer")
			}
		}

		log.Info("Cleanup completed")
		return nil
	}

	// Set up graceful shutdown
	shutdown.GracefulShutdown(log, shutdownComplete, func(shutdownCtx context.Context) error {
		// Run cleanup before process termination to avoid race condition
		if cleanupErr := cleanup(shutdownCtx); cleanupErr != nil {
			log.WithError(cleanupErr).Error("Failed to cleanup resources during signal shutdown")
		}
		return nil
	})

	log.Info("Worker server started, waiting for shutdown signal...")

	// Wait for either error or shutdown completion
	var serverErr error
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("Worker failed: %v", err)
			serverErr = err // Store error to return later

			// Cleanup only for real error path; signal/cancellation path is handled in the callback
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cleanupCancel()
			if cleanupErr := cleanup(cleanupCtx); cleanupErr != nil {
				log.WithError(cleanupErr).Error("Failed to cleanup resources on error path")
			}
		}
	case <-shutdownComplete:
		// Graceful shutdown completed (cleanup already ran in signal callback)
	}

	log.Info("Worker stopped, exiting...")
	return serverErr
}
