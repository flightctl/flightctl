package main

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/cmdsetup"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	imagebuilderworker "github.com/flightctl/flightctl/internal/imagebuilder_worker"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
)

func main() {
	ctx, cfg, log, shutdown := cmdsetup.InitService(context.Background(), "imagebuilder-worker")
	defer shutdown()

	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)

	log.SetLevel(cfg.ImageBuilderWorker.LogLevel.ToLevelWithDefault(log.Level))

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
}
