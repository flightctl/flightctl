package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	instpprof "github.com/flightctl/flightctl/internal/instrumentation/pprof"
	"github.com/flightctl/flightctl/internal/instrumentation/profiling"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	canarystore "github.com/flightctl/flightctl/internal/store/canary"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
)

func main() {
	ctx := context.Background()

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().Fatalf("reading configuration: %v", err)
	}

	log := log.InitLogs(cfg.ImageBuilderService.LogLevel)
	log.Println("Starting ImageBuilder API service")
	defer log.Println("ImageBuilder API service stopped")
	log.Printf("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-imagebuilder-api")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()
	profiling.Start(ctx, log, cfg, "flightctl-imagebuilder-api", instpprof.DefaultPortImageBuilderAPI)

	if err := encryption.InitGlobalEncryption(log, cfg); err != nil {
		log.Fatalf("initializing encryption: %v", err)
	}

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	// ImageBuilder-specific store
	imageBuilderStore := imagebuilderstore.NewStore(db, log.WithField("pkg", "imagebuilder-store"))
	defer imageBuilderStore.Close()

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

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	processID := fmt.Sprintf("imagebuilder-api-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("failed connecting to Redis queue: %v", err)
	}

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("creating kvstore: %v", err)
	}

	server, err := imagebuilderapi.New(log, cfg, imageBuilderStore, db, kvStore, provider)
	if err != nil {
		log.Fatalf("Error creating server: %s", err)
	}
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}
