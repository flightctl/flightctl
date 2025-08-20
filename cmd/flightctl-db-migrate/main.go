package main

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func main() {
	ctx := context.Background()
	// Bypass span check for migration operations
	ctx = store.WithBypassSpanCheck(ctx)

	log := log.InitLogs()
	log.Println("Starting Flight Control database migration")
	defer log.Println("Flight Control database migration completed")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-db-migrate")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	log.Println("Initializing migration database connection")
	migrationDB, err := store.InitMigrationDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing migration database: %v", err)
	}
	defer func() {
		if sqlDB, err := migrationDB.DB(); err != nil {
			log.Printf("Failed to get database connection for cleanup: %v", err)
		} else {
			sqlDB.Close()
		}
	}()

	log.Println("Running database migrations with migration user")
	// Run all schema changes atomically so that a failure leaves the DB unchanged.
	if err := migrationDB.Transaction(func(tx *gorm.DB) error {
		// Create a temporary store bound to the transaction and run migrations
		return store.NewStore(tx, log.WithField("pkg", "migration-store-tx")).RunMigrations(ctx)
	}); err != nil {
		log.Fatalf("running database migrations: %v", err)
	}

	log.Println("Database migration completed successfully")
}
