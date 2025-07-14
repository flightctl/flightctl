package main

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
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

	// Create store with migration database connection
	migrationStore := store.NewStore(migrationDB, log.WithField("pkg", "migration-store"))
	defer migrationStore.Close()

	// Run migrations using the public interface method
	log.Println("Running database migrations with migration user")
	if err := migrationStore.RunMigrations(ctx); err != nil {
		log.Fatalf("running database migrations: %v", err)
	}

	log.Println("Database migration completed successfully")
}
