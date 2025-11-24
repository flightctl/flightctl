package main

import (
	"context"
	"errors"
	"flag"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// errDryRunComplete signals that migrations validated successfully in dry-run mode.
var errDryRunComplete = errors.New("dry-run complete")

func main() {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("reading configuration")
	}

	logger := log.InitLogs(cfg.Service.LogLevel)

	dryRun := flag.Bool("dry-run", false, "Validate migrations without committing any changes")
	flag.Parse()

	ctx := context.Background()
	// Bypass span check for migration operations
	ctx = store.WithBypassSpanCheck(ctx)

	startMsg := "Starting Flight Control database migration"
	if *dryRun {
		startMsg += " in dry-run mode"
	}
	logger.Info(startMsg)
	defer logger.Info("Flight Control database migration completed")

	logger.Infof("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-db-migrate")
	defer func() {
		if err = tracerShutdown(ctx); err != nil {
			logger.WithError(err).Fatal("failed to shut down tracer")
		}
	}()

	logger.Info("Initializing migration database connection")
	migrationDB, err := store.InitMigrationDB(cfg, logger)
	if err != nil {
		logger.WithError(err).Fatal("initializing migration database")
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

	if *dryRun {
		logger.Info("Dry-run mode enabled: changes will be rolled back after validation")
	} else {
		logger.Info("Running database migrations with migration user")
	}
	// Run all schema changes atomically so that a failure leaves the DB unchanged.
	if err = migrationDB.Transaction(func(tx *gorm.DB) error {
		// Create a temporary store bound to the transaction and run migrations
		if err = store.NewStore(tx, logger.WithFields(logrus.Fields{
			"pkg":     "migration-store-tx",
			"dry_run": *dryRun,
		})).RunMigrations(ctx); err != nil {
			return err // rollback
		}
		if *dryRun {
			return errDryRunComplete // rollback but indicate success
		}
		return nil // commit
	}); err != nil {
		if errors.Is(err, errDryRunComplete) {
			logger.Info("Dry-run completed successfully; no changes were committed.")
			return
		}
		logger.WithError(err).Fatal("running database migrations")
	}

	logger.Info("Database migration completed successfully")
}
