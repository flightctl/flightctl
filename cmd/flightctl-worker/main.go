package main

import (
	"context"
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

	logger.Info("Starting worker service")
	defer logger.Info("Worker service stopped")
	logger.Infof("Using config: %s", cfg)

	// Simple context for initialization phase
	initCtx := context.Background()
	initCtx = context.WithValue(initCtx, consts.InternalRequestCtxKey, true)
	initCtx = context.WithValue(initCtx, consts.EventSourceComponentCtxKey, "flightctl-worker")
	initCtx = context.WithValue(initCtx, consts.EventActorCtxKey, "service:flightctl-worker")

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-worker")
	if tracerShutdown != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tracerShutdown(ctx); err != nil {
				logger.WithError(err).Error("Error shutting down tracer")
			}
		}()
	}

	logger.Info("Initializing data store")
	db, err := store.InitDB(cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, logger.WithField("pkg", "store"))
	defer store.Close()

	processID := fmt.Sprintf("worker-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(initCtx, logger, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return fmt.Errorf("failed connecting to Redis queue: %w", err)
	}
	// Proper provider lifecycle management
	defer provider.Wait() // Wait for graceful shutdown of background tasks
	defer provider.Stop() // Stop accepting new tasks

	k8sClient, err := k8sclient.NewK8SClient()
	if err != nil {
		logger.WithError(err).Warning("initializing k8s client, assuming k8s is not supported")
		k8sClient = nil
	}

	// Initialize metrics collectors
	var workerCollector *worker.WorkerCollector
	var systemMetricsCollector *metrics.SystemCollector
	var collectors []prometheus.Collector
	metricsServerStarted := false

	if cfg.Metrics != nil && cfg.Metrics.Enabled {
		if cfg.Metrics.WorkerCollector != nil && cfg.Metrics.WorkerCollector.Enabled {
			workerCollector = worker.NewWorkerCollector(initCtx, logger, cfg, provider)
			collectors = append(collectors, workerCollector)
		}

		if cfg.Metrics.SystemCollector != nil && cfg.Metrics.SystemCollector.Enabled {
			if systemMetricsCollector = metrics.NewSystemCollector(initCtx, cfg); systemMetricsCollector != nil {
				collectors = append(collectors, systemMetricsCollector)
				defer func() {
					if err := systemMetricsCollector.Shutdown(); err != nil {
						logger.WithError(err).Error("Error shutting down system metrics collector")
					}
				}()
			}
		}

		if len(collectors) > 0 {
			metricsServerStarted = true
		}
	}

	server := workerserver.New(cfg, logger, store, provider, k8sClient, workerCollector)

	// Define servers to coordinate
	var servers []shutdown.ServerSpec

	servers = append(servers, shutdown.ServerSpec{
		Name:   "worker server",
		Runner: server.Run,
	})

	if metricsServerStarted {
		servers = append(servers, shutdown.ServerSpec{
			Name:      "metrics server",
			IsMetrics: true, // Gets 60s grace period for shutdown metrics
			Runner: func(ctx context.Context) error {
				return tracing.RunMetricsServer(ctx, logger, cfg.Metrics.Address, collectors...)
			},
		})
	}

	// Use multi-server shutdown coordination with initCtx to preserve context values
	multiServerConfig := shutdown.NewMultiServerConfig("worker service", logger)
	return multiServerConfig.RunMultiServer(initCtx, servers)
}
