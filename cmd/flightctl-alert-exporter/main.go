package main

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
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
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func main() {
	log := log.InitLogs()

	if err := runCmd(log); err != nil {
		log.Fatalf("Alert exporter error: %v", err)
	}
}

func runCmd(log *logrus.Logger) error {
	log.Info("Starting alert exporter")
	defer log.Info("Alert exporter stopped")

	// Single context with signal handling - OS signal cancels context
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)

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

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-alert-exporter")
	if tracerShutdown != nil {
		cleanupFuncs = append(cleanupFuncs, func() error {
			log.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)

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

	processID := fmt.Sprintf("alert-exporter-%s-%s", util.GetHostname(), uuid.New().String())
	queuesProvider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return fmt.Errorf("initializing queue provider: %w", err)
	}
	cleanupFuncs = append(cleanupFuncs, func() error {
		log.Info("Stopping queue provider")
		queuesProvider.Stop()
		queuesProvider.Wait()
		return nil
	})

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		return fmt.Errorf("initializing kv store: %w", err)
	}
	cleanupFuncs = append(cleanupFuncs, func() error {
		log.Info("Closing KV store connections")
		kvStore.Close()
		return nil
	})

	publisher, err := worker_client.QueuePublisher(ctx, queuesProvider)
	if err != nil {
		return fmt.Errorf("initializing task queue publisher: %w", err)
	}
	cleanupFuncs = append(cleanupFuncs, func() error {
		log.Info("Closing publisher")
		publisher.Close()
		return nil
	})

	workerClient := worker_client.NewWorkerClient(publisher, log)

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	go func() {
		orgCache.Start(ctx)
		if ctx.Err() == nil {
			log.Warn("Organization cache stopped unexpectedly")
		}
	}()
	cleanupFuncs = append(cleanupFuncs, func() error {
		log.Info("Stopping organization cache")
		orgCache.Stop()
		return nil
	})

	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(store, workerClient, kvStore, nil, log, "", "", []string{}))
	server := alert_exporter.New(cfg, log)

	// Start server and wait for completion or signal
	log.Info("Starting alert exporter server")
	err = server.Run(ctx, serviceHandler)
	if err != nil {
		err = fmt.Errorf("alert exporter server: %w", err)
	}

	// Handle shutdown reason
	if errors.Is(err, context.Canceled) {
		log.Info("Server stopped due to shutdown signal")
		return nil // Normal shutdown
	} else if err != nil {
		log.WithError(err).Error("Server stopped with error")
		return err // Error shutdown
	}

	log.Info("Server stopped normally")
	return nil // Normal completion
}
