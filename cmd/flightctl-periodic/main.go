package main

import (
	"context"

	"github.com/flightctl/flightctl/internal/cmdsetup"
	periodic "github.com/flightctl/flightctl/internal/periodic_checker"
	"github.com/flightctl/flightctl/internal/store"
)

func main() {
	ctx, cfg, log, shutdown := cmdsetup.InitService(context.Background(), "periodic")
	defer shutdown()

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	defer store.Close()

	server := periodic.New(cfg, log, store)
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}
