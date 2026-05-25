package main

import (
	"context"
	"errors"
	"flag"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/migration"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

func main() {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("reading configuration")
	}

	log := log.InitLogs(cfg.Service.LogLevel)

	dryRun := flag.Bool("dry-run", false, "Validate migrations without committing any changes")
	flag.Parse()

	ctx := context.Background()

	startMsg := "Starting Flight Control database migration"
	if *dryRun {
		startMsg += " in dry-run mode"
	}
	log.Info(startMsg)
	defer log.Info("Flight Control database migration completed")

	log.Infof("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-db-migrate")
	defer func() {
		if err = tracerShutdown(ctx); err != nil {
			log.WithError(err).Fatal("failed to shut down tracer")
		}
	}()

	log.Info("Initializing migration database connection")
	migrationDB, err := store.InitMigrationDB(cfg, log)
	if err != nil {
		log.WithError(err).Fatal("initializing migration database")
	}
	if log.IsLevelEnabled(logrus.DebugLevel) {
		migrationDB = migrationDB.Debug()
	}
	defer func() {
		if sqlDB, err := migrationDB.DB(); err != nil {
			log.WithError(err).Warn("failed to get database connection for cleanup")
		} else {
			if err := sqlDB.Close(); err != nil {
				log.WithError(err).Warn("failed to close database connection")
			}
		}
	}()

	if *dryRun {
		log.Info("Dry-run mode enabled: changes will be rolled back after validation")
	} else {
		log.Info("Running database migrations with migration user")
	}

	if err = migration.Run(ctx, migrationDB, log, *dryRun); err != nil {
		if errors.Is(err, migration.ErrDryRunComplete) {
			log.Info("Dry-run completed successfully; no changes were committed.")
			return
		}
		log.WithError(err).Fatal("running database migrations")
	}

	log.Info("Database migration completed successfully")
}
