package main

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

func main() {
	ctx := context.Background()
	// Bypass span check for restore operations
	ctx = store.WithBypassSpanCheck(ctx)

	log := log.InitLogs()
	log.Println("Starting Flight Control restore preparation")
	defer log.Println("Flight Control restore preparation completed")

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

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-restore")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	log.Println("Initializing database connection for restore operations")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing database: %v", err)
	}
	defer func() {
		if sqlDB, err := db.DB(); err != nil {
			log.Printf("Failed to get database connection for cleanup: %v", err)
		} else {
			sqlDB.Close()
		}
	}()

	log.Println("Initializing KV store connection for restore operations")
	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("initializing KV store: %v", err)
	}
	defer kvStore.Close()

	log.Println("Creating store and service handler")
	storeInst := store.NewStore(db, log)
	serviceHandler := service.NewServiceHandler(storeInst, nil, kvStore, nil, log, "", "", []string{})

	log.Println("Running post-restoration device preparation")
	if err := serviceHandler.PrepareDevicesAfterRestore(ctx); err != nil {
		log.Fatalf("preparing devices after restore: %v", err)
	}

	log.Println("Post-restoration device preparation completed successfully")
}
