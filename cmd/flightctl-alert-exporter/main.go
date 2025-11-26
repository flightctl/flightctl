package main

import (
	"context"
	"fmt"
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
)

func main() {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("reading configuration")
	}

	if err = runCmd(cfg); err != nil {
		log.InitLogs().WithError(err).Fatal("Alert exporter error")
	}
}

func runCmd(cfg *config.Config) error {
	logger := log.InitLogs(cfg.Service.LogLevel)

	logger.Info("Starting alert exporter")
	defer logger.Info("Alert exporter stopped")
	logger.Infof("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-alert-exporter")
	if tracerShutdown != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tracerShutdown(ctx); err != nil {
				logger.WithError(err).Error("Error shutting down tracer")
			}
		}()
	}

	// Simple context for initialization phase
	initCtx := context.Background()
	initCtx = context.WithValue(initCtx, consts.EventSourceComponentCtxKey, "flightctl-alert-exporter")
	initCtx = context.WithValue(initCtx, consts.EventActorCtxKey, "service:flightctl-alert-exporter")
	initCtx = context.WithValue(initCtx, consts.InternalRequestCtxKey, true)

	logger.Info("Initializing data store")
	db, err := store.InitDB(cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, logger.WithField("pkg", "store"))
	defer store.Close()

	processID := fmt.Sprintf("alert-exporter-%s-%s", util.GetHostname(), uuid.New().String())
	queuesProvider, err := queues.NewRedisProvider(initCtx, logger, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return fmt.Errorf("initializing queue provider: %w", err)
	}
	defer queuesProvider.Wait()
	defer queuesProvider.Stop()

	kvStore, err := kvstore.NewKVStore(initCtx, logger, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		return fmt.Errorf("initializing kv store: %w", err)
	}
	defer kvStore.Close()

	publisher, err := worker_client.QueuePublisher(initCtx, queuesProvider)
	if err != nil {
		return fmt.Errorf("initializing task queue publisher: %w", err)
	}
	defer publisher.Close()

	workerClient := worker_client.NewWorkerClient(publisher, logger)

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	defer orgCache.Stop()

	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(store, workerClient, kvStore, nil, logger, "", "", []string{}))
	server := alert_exporter.New(cfg, logger)

	// Use single server shutdown coordination
	singleServerConfig := shutdown.NewSingleServerConfig("alert exporter", logger)
	return singleServerConfig.RunSingleServer(func(shutdownCtx context.Context) error {
		return server.Run(shutdownCtx, serviceHandler)
	})
}
