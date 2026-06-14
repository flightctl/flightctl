package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	remoteaccessserver "github.com/flightctl/flightctl/internal/remote_access_server"
	"github.com/flightctl/flightctl/pkg/log"
)

func main() {
	ctx := context.Background()

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().Fatalf("reading configuration: %v", err)
	}

	log := log.InitLogs(cfg.Service.LogLevel)
	log.Println("Starting remote-access service")
	defer log.Println("Remote-access service stopped")
	log.Printf("Using config: %s", cfg)

	ca, err := crypto.LoadInternalCA(cfg.CA)
	if err != nil {
		log.Fatalf("loading CA certificates: %v", err)
	}
	caClient := crypto.NewCAClient(cfg.CA, ca)

	serverCerts, err := config.LoadServerCertificates(cfg, log)
	if err != nil {
		log.Fatalf("loading server certificates: %v", err)
	}

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-remote-access")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	server, err := remoteaccessserver.New(log, cfg, caClient, serverCerts)
	if err != nil {
		log.Fatalf("initializing remote-access server: %v", err)
	}

	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running remote-access server: %v", err)
	}
}
