package main

import (
	"context"
	"errors"
	"flag"

	dbmigrateconfig "github.com/flightctl/flightctl/internal/config/dbmigrate"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// runImageBuildMigrations runs migrations for the ImageBuild store
func runImageBuildMigrations(ctx context.Context, db *gorm.DB, log logrus.FieldLogger) error {
	imageBuildStore := imagebuilderstore.NewStore(db, log.WithField("pkg", "imagebuild-migration"))
	return imageBuildStore.RunMigrations(ctx)
}

// errDryRunComplete signals that migrations validated successfully in dry-run mode.
var errDryRunComplete = errors.New("dry-run complete")

func main() {
	cfg, err := dbmigrateconfig.LoadOrGenerate(dbmigrateconfig.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("reading configuration")
	}

	log := log.InitLogs(cfg.LogLevel())

	dryRun := flag.Bool("dry-run", false, "Validate migrations without committing any changes")
	flag.Parse()

	ctx := context.Background()
	// Bypass span check for migration operations
	ctx = store.WithBypassSpanCheck(ctx)

	startMsg := "Starting Flight Control database migration"
	if *dryRun {
		startMsg += " in dry-run mode"
	}
	log.Info(startMsg)
	defer log.Info("Flight Control database migration completed")

	log.Infof("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(log, cfg.TracingConfig(), "flightctl-db-migrate")
	defer func() {
		if err = tracerShutdown(ctx); err != nil {
			log.WithError(err).Fatal("failed to shut down tracer")
		}
	}()

	log.Info("Initializing migration database connection")
	migrationDB, err := store.InitMigrationDB(cfg.DatabaseConfig(), cfg.TracingConfig(), log)
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
	// Run all schema changes atomically so that a failure leaves the DB unchanged.
	if err = migrationDB.Transaction(func(tx *gorm.DB) error {
		// Create a temporary store bound to the transaction and run migrations
		if err = store.NewStore(tx, log.WithFields(logrus.Fields{
			"pkg":     "migration-store-tx",
			"dry_run": *dryRun,
		})).RunMigrations(ctx); err != nil {
			return err // rollback
		}

		// Run ImageBuild migrations (separate store for imagebuilder-api)
		if err = runImageBuildMigrations(ctx, tx, log.WithFields(logrus.Fields{
			"pkg":     "imagebuild-migration-tx",
			"dry_run": *dryRun,
		})); err != nil {
			return err // rollback
		}

		if *dryRun {
			return errDryRunComplete // rollback but indicate success
		}
		return nil // commit
	}); err != nil {
		if errors.Is(err, errDryRunComplete) {
			log.Info("Dry-run completed successfully; no changes were committed.")
			return
		}
		log.WithError(err).Fatal("running database migrations")
	}

	log.Info("Database migration completed successfully")
}
