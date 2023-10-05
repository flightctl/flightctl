package main

import (
	"log"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
)

func main() {
	log.Println("Starting device management service")
	defer log.Println("Device management service stopped")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}
	log.Printf("Using config: %s", cfg)

	log.Println("Initializing data store...")
	db, err := store.InitDB(cfg)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	store := store.NewStore(db)
	if err := store.InitialMigration(); err != nil {
		log.Fatalf("running initial migration: %v", err)
	}
}
