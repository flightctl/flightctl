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

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-periodic")
	if tracerShutdown != nil {
		cleanupFuncs = append(cleanupFuncs, func() error {
			log.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	log.Info("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	cleanupFuncs = append(cleanupFuncs, func() error {
		log.Info("Closing database connections")
		store.Close()
		return nil
	})

	server := periodic.New(cfg, log, store)

	// Start server and wait for completion or signal
	log.Info("Starting periodic server")
	err = server.Run(ctx)
	if err != nil {
		err = fmt.Errorf("periodic server: %w", err)
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
