package main

import (
	"context"
	"fmt"

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

	// Create shutdown manager for coordinated shutdown
	shutdownManager := shutdown.NewShutdownManager(log)

	shutdown.HandleSignalsWithManager(log, shutdownManager, shutdown.DefaultGracefulShutdownTimeout)
	if err := runCmd(shutdownManager, log); err != nil {
		log.Fatalf("Worker service error: %v", err)
	}
}

func runCmd(shutdownManager *shutdown.ShutdownManager, log *logrus.Logger) error {
	log.Info("Starting worker service")
	defer log.Info("Worker service stopped")

	ctx := context.Background()

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
	defer func() {
		if err := tracerShutdown(context.Background()); err != nil {
			log.Errorf("failed to shut down tracer: %v", err)
		}
	}()

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return err
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	defer store.Close()

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
				defer func() {
					if err := systemMetricsCollector.Shutdown(); err != nil {
						log.Errorf("Failed to shutdown system metrics collector: %v", err)
					}
				}()
			}
		}

		if len(collectors) > 0 {
			// Start metrics server in background
			go func() {
				log.Info("Starting metrics server")
				metricsServer := metrics.NewMetricsServer(log, collectors...)
				if err := metricsServer.Run(ctx); err != nil {
					log.Errorf("Metrics server error: %v", err)
					shutdownManager.TriggerFailFast("metrics-server", err)
				}
			}()
		}
	}

	server := workerserver.New(cfg, log, store, provider, k8sClient, workerCollector)

	// Register cleanup functions with shutdown manager
	shutdownManager.Register("worker-server", shutdown.PriorityHigh, shutdown.TimeoutServer, func(ctx context.Context) error {
		log.Info("Stopping worker server")
		// TODO: Add graceful shutdown support to worker server
		return nil
	})

	shutdownManager.Register("queues", shutdown.PriorityLowest, shutdown.TimeoutDatabase, func(ctx context.Context) error {
		log.Info("Stopping queue provider")
		provider.Stop()
		provider.Wait()
		return nil
	})

	shutdownManager.Register("database", shutdown.PriorityLowest, shutdown.TimeoutDatabase, func(ctx context.Context) error {
		log.Info("Closing database connections")
		store.Close()
		return nil
	})

	shutdownManager.Register("tracer", shutdown.PriorityLowest, shutdown.TimeoutStandard, func(ctx context.Context) error {
		log.Info("Shutting down tracer")
		return tracerShutdown(context.Background())
	})

	// Start worker server
	go func() {
		log.Info("Starting worker server")
		if err := server.Run(ctx); err != nil {
			log.Errorf("Worker server error: %v", err)
			shutdownManager.TriggerFailFast("worker-server", err)
		}
	}()

	log.WithField("metrics_enabled", cfg.Metrics != nil && cfg.Metrics.Enabled).Info("Worker server started, waiting for shutdown signal...")

	// Create a done channel that will be closed when shutdown is complete
	done := make(chan struct{})

	// Register a final shutdown callback that signals completion
	shutdownManager.Register("completion", shutdown.PriorityLast, shutdown.TimeoutCompletion, func(ctx context.Context) error {
		close(done)
		return nil
	})

	// Wait for shutdown completion
	<-done
	log.Info("Graceful shutdown completed")

	return nil
}
