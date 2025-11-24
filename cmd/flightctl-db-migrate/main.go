package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// errDryRunComplete signals that migrations validated successfully in dry-run mode.
var errDryRunComplete = errors.New("dry-run complete")

func main() {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("Failed to load configuration")
	}

	logger := log.InitLogs(cfg.Service.LogLevel)

	dryRun := flag.Bool("dry-run", false, "Validate migrations without committing any changes")
	flag.Parse()

	startMsg := "Starting Flight Control database migration"
	if *dryRun {
		startMsg += " in dry-run mode"
	}
	logger.Info(startMsg)
	defer logger.Info("Flight Control database migration completed")

	logger.Infof("Using config: %s", cfg)

	// Use single-server shutdown pattern for graceful cancellation of migrations
	shutdownConfig := shutdown.NewSingleServerConfig("database migration", logger)
	if err := shutdownConfig.RunSingleServer(func(shutdownCtx context.Context) error {
		return runMigration(shutdownCtx, cfg, logger, *dryRun)
	}); err != nil {
		logger.WithError(err).Fatal("Database migration error")
	}
}

func runMigration(shutdownCtx context.Context, cfg *config.Config, logger *logrus.Logger, dryRun bool) error {
	ctx := shutdownCtx
	// Bypass span check for migration operations
	ctx = store.WithBypassSpanCheck(ctx)

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-db-migrate")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracerShutdown(ctx); err != nil {
			logger.WithError(err).Error("Error shutting down tracer")
		}
	}()

	logger.Info("Initializing migration database connection")
	migrationDB, err := store.InitMigrationDB(cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing migration database: %w", err)
	}
	if logger.IsLevelEnabled(logrus.DebugLevel) {
		migrationDB = migrationDB.Debug()
	}
	defer func() {
		if sqlDB, err := migrationDB.DB(); err != nil {
			logger.WithError(err).Warn("failed to get database connection for cleanup")
		} else {
			if err := sqlDB.Close(); err != nil {
				logger.WithError(err).Warn("failed to close database connection")
			}
		}
	}()

	if dryRun {
		logger.Info("Dry-run mode enabled: changes will be rolled back after validation")
	} else {
		logger.Info("Running database migrations with migration user")
	}

	// Run all schema changes atomically so that a failure leaves the DB unchanged.
	// Pass shutdown context so migration can be cancelled
	if err := migrationDB.Transaction(func(tx *gorm.DB) error {
		// Create a temporary store bound to the transaction and run migrations
		if err := store.NewStore(tx, logger.WithFields(logrus.Fields{
			"pkg":     "migration-store-tx",
			"dry_run": dryRun,
		})).RunMigrations(ctx); err != nil {
			return err // rollback
		}
		if dryRun {
			return errDryRunComplete // rollback but indicate success
		}
		return nil // commit
	}); err != nil {
		if errors.Is(err, errDryRunComplete) {
			logger.Info("Dry-run completed successfully; no changes were committed.")
			return nil
		}
		return fmt.Errorf("running database migrations: %w", err)
	}

	logger.Info("Database migration completed successfully")
	return nil
}
