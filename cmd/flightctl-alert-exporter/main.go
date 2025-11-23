package main

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/alert_exporter"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func main() {
	logger := log.InitLogs()

	if err := runCmd(logger); err != nil {
		logger.Fatalf("Alert exporter error: %v", err)
	}
}

func runCmd(log *logrus.Logger) error {
	log.Info("Starting alert exporter")
	defer log.Info("Alert exporter stopped")

	// Create shutdown manager with explicit signals (no SIGHUP) and timeout for alert processing
	shutdownMgr := shutdown.NewManager(log).
		WithSignals(syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT).
		WithTimeout(shutdown.DefaultShutdownTimeout)

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
	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-alert-exporter")
	if tracerShutdown != nil {
		shutdownMgr.AddCleanup("tracer", func() error {
			log.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	// Setup context with service metadata
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)

	// Initialize data store with cleanup
	log.Info("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	shutdownMgr.AddCleanup("database", shutdown.DatabaseCloseFunc(log, store.Close))

	// Initialize queue provider with cleanup
	processID := fmt.Sprintf("alert-exporter-%s-%s", util.GetHostname(), uuid.New().String())
	queuesProvider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return fmt.Errorf("initializing queue provider: %w", err)
	}
	shutdownMgr.AddCleanup("queue-provider", shutdown.StopWaitFunc("queue-provider", queuesProvider.Stop, queuesProvider.Wait))

	// Initialize KV store with cleanup
	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		return fmt.Errorf("initializing kv store: %w", err)
	}
	shutdownMgr.AddCleanup("kv-store", shutdown.CloseFunc(kvStore.Close))

	// Initialize publisher with cleanup
	publisher, err := worker_client.QueuePublisher(ctx, queuesProvider)
	if err != nil {
		return fmt.Errorf("initializing task queue publisher: %w", err)
	}
	shutdownMgr.AddCleanup("publisher", shutdown.CloseFunc(publisher.Close))

	// Initialize worker client
	workerClient := worker_client.NewWorkerClient(publisher, log)

	// Initialize organization cache with cleanup
	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	go func() {
		orgCache.Start(ctx)
		if ctx.Err() == nil {
			log.Warn("Organization cache stopped unexpectedly")
		}
	}()
	shutdownMgr.AddCleanup("org-cache", shutdown.CloseFunc(orgCache.Stop))

	// Setup alert exporter server
	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(store, workerClient, kvStore, nil, log, "", "", []string{}))
	server := alert_exporter.New(cfg, log)

	// Add server to the shutdown manager
	shutdownMgr.AddServer("alert-exporter", shutdown.NewServerFunc(func(ctx context.Context) error {
		return server.Run(ctx, serviceHandler)
	}))

	// Run with coordinated shutdown - all the complex error handling is now internal
	return shutdownMgr.Run(ctx)
}
