package main

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	periodic "github.com/flightctl/flightctl/internal/periodic_checker"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
)

func main() {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("reading configuration")
	}

	if err := runCmd(cfg); err != nil {
		log.InitLogs().WithError(err).Fatal("Periodic service error")
	}
}

func runCmd(cfg *config.Config) error {
	logger := log.InitLogs(cfg.Service.LogLevel)

	logger.Info("Starting periodic service")
	defer logger.Info("Periodic service stopped")
	logger.Infof("Using config: %s", cfg)

	// Single context with signal handling - OS signal cancels context
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)

	// Build cleanup functions incrementally as resources are created
	var cleanupFuncs []func() error
	defer func() {
		// First cancel context to signal all goroutines to stop
		logger.Info("Cancelling context to stop all servers")
		cancel()

		// Then run cleanup in reverse order after goroutines have stopped
		logger.Info("Starting cleanup")
		for i := len(cleanupFuncs) - 1; i >= 0; i-- {
			if err := cleanupFuncs[i](); err != nil {
				logger.WithError(err).Error("Cleanup error")
			}
		}
		logger.Info("Cleanup completed")
	}()

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-periodic")
	if tracerShutdown != nil {
		cleanupFuncs = append(cleanupFuncs, func() error {
			logger.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	logger.Info("Initializing data store")
	db, err := store.InitDB(cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, logger.WithField("pkg", "store"))
	cleanupFuncs = append(cleanupFuncs, func() error {
		logger.Info("Closing database connections")
		store.Close()
		return nil
	})

	server := periodic.New(cfg, logger, store)

	// Start server and wait for completion or signal
	logger.Info("Starting periodic server")
	err = server.Run(ctx)
	if err != nil {
		err = fmt.Errorf("periodic server: %w", err)
	}

	// Handle shutdown reason
	if errors.Is(err, context.Canceled) {
		logger.Info("Server stopped due to shutdown signal")
		return nil // Normal shutdown
	} else if err != nil {
		logger.WithError(err).Error("Server stopped with error")
		return err // Error shutdown
	}

	logger.Info("Server stopped normally")
	return nil // Normal completion
}
