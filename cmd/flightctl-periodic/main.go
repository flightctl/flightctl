package main

import (
	"context"
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

	// Create shutdown manager for coordinated shutdown with special periodic handling
	shutdownManager := shutdown.NewShutdownManager(log)

	shutdown.HandleSignalsWithManager(log, shutdownManager, shutdown.DefaultGracefulShutdownTimeout)
	if err := runCmd(shutdownManager, log); err != nil {
		log.Fatalf("Periodic service error: %v", err)
	}
}

func runCmd(shutdownManager *shutdown.ShutdownManager, log *logrus.Logger) error {
	log.Info("Starting periodic service")
	defer log.Info("Periodic service stopped")

	ctx := context.Background()
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel() // Ensure context is always canceled

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

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return err
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))

	// Register database cleanup
	shutdownManager.Register("database", shutdown.PriorityLow, shutdown.TimeoutDatabase, func(ctx context.Context) error {
		log.Info("Closing database connections")
		store.Close()
		return nil
	})

	// Register tracer shutdown
	shutdownManager.Register("tracer", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error {
		log.Info("Shutting down tracer")
		return tracerShutdown(ctx)
	})

	server := periodic.New(cfg, log, store)

	// Start periodic server
	go func() {
		log.Info("Starting periodic server")
		if err := server.Run(serverCtx); err != nil {
			log.Errorf("Periodic server error: %v", err)
		}
	}()

	// Register special periodic server shutdown with extended timeout for final stop
	// This is the key "wait for periodic to finally stop" behavior
	shutdownManager.Register("periodic-server", shutdown.PriorityHigh, shutdown.TimeoutPeriodic, func(ctx context.Context) error {
		log.Info("Initiating periodic server shutdown - waiting for tasks to complete")

		// Cancel the server context to start graceful shutdown
		serverCancel()

		// The periodic server implementation has its own internal WaitGroup and shutdown logic
		// We respect the context timeout but give it extra time since periodic tasks may need
		// to finish their current work cycles before stopping

		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				log.Warn("Periodic server shutdown timeout exceeded - some tasks may not have completed")
			}
			return ctx.Err()
		default:
			// Wait for a reasonable time for the server to complete its shutdown
			// The periodic server handles its own graceful shutdown when context is cancelled
			log.Info("Waiting for periodic server to complete final stop...")
			time.Sleep(2 * time.Second) // Brief wait to let server complete
			log.Info("Periodic server graceful shutdown completed")
			return nil
		}
	})

	// Create a done channel that will be closed when shutdown is complete
	done := make(chan struct{})

	// Register a final shutdown callback that signals completion
	shutdownManager.Register("completion", shutdown.PriorityLast, shutdown.TimeoutCompletion, func(ctx context.Context) error {
		close(done)
		return nil
	})

	log.Info("Periodic server started, waiting for shutdown signal...")

	// Wait for shutdown to complete with 5-minute timeout
	shutdownTimeoutCtx, cancel := context.WithTimeout(context.Background(), shutdown.TimeoutPeriodicServiceShutdown)
	defer cancel()

	select {
	case <-done:
		cancel() // Clean up context resources immediately
		log.Info("All periodic service components shut down successfully")
		return nil
	case <-shutdownTimeoutCtx.Done():
		log.WithField("timeout", shutdown.TimeoutPeriodicServiceShutdown).Error("Periodic service shutdown timeout exceeded - forcing exit")
		return context.DeadlineExceeded
	}
}
