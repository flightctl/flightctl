package main

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/cmdsetup"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
)

func main() {
	ctx, cfg, log, shutdown := cmdsetup.InitService(context.Background(), "imagebuilder-api")
	defer shutdown()

	log.SetLevel(cfg.ImageBuilderService.LogLevel.ToLevelWithDefault(log.Level))

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	// ImageBuilder-specific store
	imageBuilderStore := imagebuilderstore.NewStore(db, log.WithField("pkg", "imagebuilder-store"))
	defer imageBuilderStore.Close()

	// Main store for identity mapping (shared with api_server)
	mainStore := store.NewStore(db, log.WithField("pkg", "store"))
	defer mainStore.Close()

	processID := fmt.Sprintf("imagebuilder-api-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("failed connecting to Redis queue: %v", err)
	}

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("creating kvstore: %v", err)
	}

	server, err := imagebuilderapi.New(log, cfg, imageBuilderStore, mainStore, kvStore, provider)
	if err != nil {
		log.Fatalf("Error creating server: %s", err)
	}
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}
