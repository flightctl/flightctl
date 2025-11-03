package main

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	periodic "github.com/flightctl/flightctl/internal/periodic_checker"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/sirupsen/logrus"
)

func main() {
	log := log.InitLogs()

	if err := runCmd(log); err != nil {
		log.Fatalf("Periodic service error: %v", err)
	}
}

func runCmd(log *logrus.Logger) error {
	log.Info("Starting periodic service")
	defer log.Info("Periodic service stopped")

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

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

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-periodic")

	log.Info("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return err
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))

	server := periodic.New(cfg, log, store)

	// Cleanup function to be called on both signal and error paths
	cleanup := func(shutdownCtx context.Context) error {
		log.Info("Starting cleanup...")

		// Cancel server context to start graceful shutdown
		log.Info("Stopping periodic server")
		serverCancel()

		// Give periodic tasks time to complete
		log.Info("Waiting for periodic tasks to complete")
		time.Sleep(2 * time.Second)

		// Close database connections
		log.Info("Closing database connections")
		store.Close()

		// Shutdown tracer
		if tracerShutdown != nil {
			log.Info("Shutting down tracer")
			if err := tracerShutdown(shutdownCtx); err != nil {
				log.WithError(err).Error("Failed to shutdown tracer")
			}
		}

		log.Info("Cleanup completed")
		return nil
	}

	// Start periodic server
	go func() {
		log.Info("Starting periodic server")
		err := server.Run(serverCtx)

		// Always signal main thread
		select {
		case errCh <- err:
		default:
		}

		// Always cancel unless it was already a context cancellation
		if !errors.Is(err, context.Canceled) {
			serverCancel() // Cancel for both error AND success cases
		}

		// Log errors for debugging
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("Periodic server error: %v", err)
		}
	}()

	// Channel to coordinate shutdown completion
	shutdownComplete := make(chan struct{})

	// Set up graceful shutdown
	shutdown.GracefulShutdown(log, shutdownComplete, func(shutdownCtx context.Context) error {
		log.Info("Graceful shutdown signal received")

		// Run cleanup before os.Exit
		if cleanupErr := cleanup(shutdownCtx); cleanupErr != nil {
			log.WithError(cleanupErr).Error("Failed to cleanup resources during signal shutdown")
		}

		return nil
	})

	log.Info("Periodic server started, waiting for shutdown signal...")

	// Wait for either error or shutdown completion
	var serverErr error
	select {
	case err := <-errCh:
		// Only treat real failures as errors, not context cancellation (normal shutdown)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("Periodic service failed: %v", err)
			serverErr = err // Store error to return later

			// Cleanup for error path only (signal path already cleaned up in callback)
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer shutdownCancel()
			if cleanupErr := cleanup(shutdownCtx); cleanupErr != nil {
				log.WithError(cleanupErr).Error("Failed to cleanup resources on error path")
			}
		}
	case <-shutdownComplete:
		// Graceful shutdown completed (cleanup already ran in signal callback)
	}

	log.Info("Periodic service stopped, exiting...")
	return serverErr
}
