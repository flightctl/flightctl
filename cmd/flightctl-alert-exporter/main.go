package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/alert_exporter"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
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
	log := log.InitLogs()

	if err := runCmd(log); err != nil {
		log.Fatalf("Alert exporter error: %v", err)
	}
}

func runCmd(log *logrus.Logger) error {
	log.Info("Starting alert exporter")
	defer log.Info("Alert exporter stopped")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)

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

	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)

	log.Info("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))

	processID := fmt.Sprintf("alert-exporter-%s-%s", util.GetHostname(), uuid.New().String())
	queuesProvider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return fmt.Errorf("initializing queue provider: %w", err)
	}

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		return fmt.Errorf("initializing kv store: %w", err)
	}

	publisher, err := worker_client.QueuePublisher(ctx, queuesProvider)
	if err != nil {
		return fmt.Errorf("initializing task queue publisher: %w", err)
	}
	workerClient := worker_client.NewWorkerClient(publisher, log)

	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(store, workerClient, kvStore, nil, log, "", "", []string{}))

	server := alert_exporter.New(cfg, log)

	// Channel to coordinate shutdown completion
	shutdownComplete := make(chan struct{})

	// Cleanup function
	cleanup := func(cleanupCtx context.Context) error {
		log.Info("Starting cleanup...")

		// Cancel ctx so any ctx-aware goroutines can react
		cancel()

		// Close resources
		log.Info("Closing publisher")
		publisher.Close()

		log.Info("Closing KV store connections")
		kvStore.Close()

		log.Info("Closing database connections")
		store.Close()

		log.Info("Stopping queue provider")
		queuesProvider.Stop()
		queuesProvider.Wait()

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

	// Start server in background
	go func() {
		log.Info("Starting alert exporter server")
		err := server.Run(ctx, serviceHandler)

		// Always signal main thread (nil for success, error for failure)
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
			log.Errorf("Alert exporter server error: %v", err)
		}
	}()

	log.Info("Alert exporter server started, waiting for shutdown signal...")

	// Wait for either error or shutdown completion
	var serverErr error
	select {
	case err := <-errCh:
		// Only treat real failures as errors, not context cancellation (normal shutdown)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("Alert exporter failed: %v", err)
			serverErr = err // Store error to return later

			// Cleanup for real error path (signal-driven shutdown already handled cleanup in callback)
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cleanupCancel()
			if cleanupErr := cleanup(cleanupCtx); cleanupErr != nil {
				log.WithError(cleanupErr).Error("Failed to cleanup resources on error path")
			}
		}
	case <-shutdownComplete:
		// Graceful shutdown completed (cleanup already ran in signal callback)
	}

	log.Info("Alert exporter stopped, exiting...")
	return serverErr
}
