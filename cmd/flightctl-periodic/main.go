package main

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	instpprof "github.com/flightctl/flightctl/internal/instrumentation/pprof"
	"github.com/flightctl/flightctl/internal/instrumentation/profiling"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	periodic "github.com/flightctl/flightctl/internal/periodic_checker"
	"github.com/flightctl/flightctl/internal/store"
	canarystore "github.com/flightctl/flightctl/internal/store/canary"
	"github.com/flightctl/flightctl/pkg/log"
)

func main() {
	ctx := context.Background()

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().Fatalf("reading configuration: %v", err)
	}

	log := log.InitLogs(cfg.Service.LogLevel)
	log.Println("Starting periodic")
	log.Printf("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-periodic")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()
	profiling.Start(ctx, log, cfg, "flightctl-periodic", instpprof.DefaultPortPeriodic)

	if err := encryption.InitGlobalEncryption(log, cfg); err != nil {
		log.Fatalf("initializing encryption: %v", err)
	}

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	if encMgr := encryption.GlobalManager(); encMgr != nil {
		canaryStore := canarystore.NewCanaryStore(db, log.WithField("pkg", "canary-store"))
		encMgr.SetCanaryStore(canarystore.AsEncryptionStore(canaryStore))
		valCtx, valCancel := context.WithTimeout(ctx, 30*time.Second)
		err := encMgr.ValidateCanaries(valCtx)
		valCancel()
		if err != nil {
			log.Fatalf("validating encryption canaries: %v", err)
		}
	}

	server := periodic.New(cfg, log, db)
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}
