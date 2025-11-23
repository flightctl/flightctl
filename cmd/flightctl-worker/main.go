package main

import (
	"context"
	"fmt"
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

	// Create shutdown manager with explicit signals (no SIGHUP) and timeout for job completion
	shutdownMgr := shutdown.NewManager(log).
		WithSignals(syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT).
		WithTimeout(shutdown.LongRunningShutdownTimeout)

	// Load configuration
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

	// Setup tracer with cleanup
	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-worker")
	if tracerShutdown != nil {
		shutdownMgr.AddCleanup("tracer", func() error {
			log.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	// Initialize data store with cleanup
	log.Info("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	shutdownMgr.AddCleanup("database", shutdown.DatabaseCloseFunc(log, store.Close))

	// Setup context with service metadata
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-worker")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-worker")

	// Initialize queue provider with cleanup and force stop capability
	processID := fmt.Sprintf("worker-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return fmt.Errorf("failed connecting to Redis queue: %w", err)
	}
	shutdownMgr.AddCleanup("queue-provider", shutdown.StopWaitFunc("queue-provider", provider.Stop, provider.Wait))

	// Set provider stop as force stop function for deadlock prevention
	shutdownMgr.WithForceStop(provider.Stop)

	k8sClient, err := k8sclient.NewK8SClient()
	if err != nil {
		log.WithError(err).Warning("initializing k8s client, assuming k8s is not supported")
		k8sClient = nil
	}

	// Initialize metrics collectors
	var workerCollector *worker.WorkerCollector
	var systemMetricsCollector *metrics.SystemCollector

	if cfg.Metrics != nil && cfg.Metrics.Enabled {
		var collectors []prometheus.Collector
		if cfg.Metrics.WorkerCollector != nil && cfg.Metrics.WorkerCollector.Enabled {
			workerCollector = worker.NewWorkerCollector(ctx, log, cfg, provider)
			collectors = append(collectors, workerCollector)
		}

		if cfg.Metrics.SystemCollector != nil && cfg.Metrics.SystemCollector.Enabled {
			if systemMetricsCollector = metrics.NewSystemCollector(ctx, cfg); systemMetricsCollector != nil {
				collectors = append(collectors, systemMetricsCollector)
				shutdownMgr.AddCleanup("system-metrics", func() error {
					log.Info("Shutting down system metrics collector")
					return systemMetricsCollector.Shutdown()
				})
			}
		}

		if len(collectors) > 0 {
			shutdownMgr.AddServer("metrics", shutdown.NewServerFunc(func(ctx context.Context) error {
				log.Info("Starting metrics server")
				return tracing.RunMetricsServer(ctx, log, cfg.Metrics.Address, collectors...)
			}))
		}
	}

	server := workerserver.New(cfg, log, store, provider, k8sClient, workerCollector)

	// Add worker server to shutdown manager
	shutdownMgr.AddServer("worker", shutdown.NewServerFunc(func(ctx context.Context) error {
		log.Info("Starting worker server")
		return server.Run(ctx)
	}))

	// Run with coordinated shutdown - all the complex error handling is now internal
	log.Info("Worker service started, waiting for shutdown signal...")
	return shutdownMgr.Run(ctx)
}
