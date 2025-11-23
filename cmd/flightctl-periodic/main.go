package main

import (
	"context"
	"fmt"
	"syscall"
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

	// Create shutdown manager with explicit signals (no SIGHUP) and longer timeout for task completion
	shutdownMgr := shutdown.NewManager(log).
		WithSignals(syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT).
		WithTimeout(shutdown.LongRunningShutdownTimeout)

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
	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-periodic")
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

	// Create and add periodic server
	server := periodic.New(cfg, log, store)
	shutdownMgr.AddServer("periodic", shutdown.NewServerFunc(func(ctx context.Context) error {
		log.Info("Starting periodic server")
		return server.Run(ctx)
	}))

	// Run with coordinated shutdown
	return shutdownMgr.Run(context.Background())
}
