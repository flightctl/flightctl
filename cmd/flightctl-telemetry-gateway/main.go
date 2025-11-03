package main

import (
	"context"
	"errors"
	"sync"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	tg "github.com/flightctl/flightctl/internal/telemetry_gateway"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/sirupsen/logrus"
)

func main() {
	ctx := context.Background()

	if err := runCmd(ctx); err != nil {
		log.InitLogs().Fatalf("flightctl-telemetry-gateway: %v", err)
	}
}

func runCmd(ctx context.Context) error {
	log := log.InitLogs()

	log.Info("Starting telemetry gateway")
	defer log.Info("Telemetry gateway stopped")

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

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-telemetry-gateway")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Use sync.Once to ensure tracer shutdown runs only once
	var shutdownOnce sync.Once

	// Tracer shutdown function with defensive guard
	shutdownTracer := func(shutdownCtx context.Context) {
		shutdownOnce.Do(func() {
			if tracerShutdown != nil {
				if err := tracerShutdown(shutdownCtx); err != nil {
					log.WithError(err).Error("Failed to shutdown tracer")
				}
			}
		})
	}

	errCh := make(chan error, 1)

	// Cleanup function to be called on both signal and error paths
	cleanup := func(cleanupCtx context.Context) error {
		log.Info("Starting cleanup...")

		// Cancel context to stop telemetry gateway
		cancel()

		// Flush tracer (idempotent via sync.Once)
		shutdownTracer(cleanupCtx)

		log.Info("Cleanup completed")
		return nil
	}

	// Start telemetry gateway in background
	go func() {
		log.Info("Starting telemetry gateway")
		err := tg.Run(ctx, cfg)

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
			log.Errorf("Telemetry gateway error: %v", err)
		}
	}()

	// Channel to coordinate shutdown completion
	shutdownComplete := make(chan struct{})

	// Set up graceful shutdown
	shutdown.GracefulShutdown(log, shutdownComplete, func(shutdownCtx context.Context) error {
		// Run cleanup for signal-driven shutdown (includes tracer flush)
		if cleanupErr := cleanup(shutdownCtx); cleanupErr != nil {
			log.WithError(cleanupErr).Error("Failed to cleanup resources during signal shutdown")
		}
		return nil
	})

	log.Info("Telemetry gateway started, waiting for shutdown signal...")

	// Wait for either error or shutdown completion
	var serverErr error
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("Telemetry gateway failed: %v", err)
			serverErr = err // Store error to return later
		}
		// Cleanup for error path (signal path already cleaned up in callback)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), shutdown.DefaultGracefulShutdownTimeout)
		defer cleanupCancel()
		if cleanupErr := cleanup(cleanupCtx); cleanupErr != nil {
			log.WithError(cleanupErr).Error("Failed to cleanup resources on error path")
		}
	case <-shutdownComplete:
		// Graceful shutdown completed (cleanup already ran in signal callback)
	}

	log.Info("Telemetry gateway stopped, exiting...")
	return serverErr
}
