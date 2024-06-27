package main

import (
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	workerserver "github.com/flightctl/flightctl/internal/worker_server"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

func main() {
	log := log.InitLogs()
	log.Println("Starting worker service")
	defer log.Println("Worker service stopped")

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

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	defer store.Close()

	server := workerserver.New(cfg, log, store)
	if err := server.Run(); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}
