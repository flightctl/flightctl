package main

import (
	"log"

	"github.com/flightctl/flightctl/internal/config"
)

func main() {
	log.Println("Starting device management service")
	defer log.Println("Device management service stopped")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}
	log.Printf("Using config: %s", cfg)
}
