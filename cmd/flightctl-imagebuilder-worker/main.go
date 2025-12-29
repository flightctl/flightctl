package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	imagebuilderworker "github.com/flightctl/flightctl/internal/imagebuilder_worker"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
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

	log := log.InitLogs(cfg.ImageBuilderWorker.LogLevel)
	log.Println("Starting ImageBuilder Worker service")
	defer log.Println("ImageBuilder Worker service stopped")
	log.Printf("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-imagebuilder-worker")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Errorf("failed to shut down tracer: %v", err)
		}
	}()

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	// ImageBuilder-specific store
	imageBuilderStore := imagebuilderstore.NewStore(db, log.WithField("pkg", "imagebuilder-store"))
	defer imageBuilderStore.Close()

	// Main store for accessing Repository and EnrollmentRequest resources
	mainStore := store.NewStore(db, log.WithField("pkg", "store"))
	defer mainStore.Close()

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	// Set internal request context values for worker operations
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-imagebuilder-worker")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-imagebuilder-worker")

	processID := fmt.Sprintf("imagebuilder-worker-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("failed connecting to Redis queue: %v", err)
	}

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("creating kvstore: %v", err)
	}

	// Initialize CA client for generating enrollment credentials
	log.Println("Initializing CA client")
	ca, err := crypto.LoadInternalCA(cfg.CA)
	if err != nil {
		log.Fatalf("loading CA certificates: %v", err)
	}
	caClient := crypto.NewCAClient(cfg.CA, ca)

	server := imagebuilderworker.New(cfg, log, imageBuilderStore, mainStore, kvStore, provider, caClient)
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
	cancel()
}
