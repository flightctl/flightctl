package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	remoteaccessserver "github.com/flightctl/flightctl/internal/remote_access_server"
	"github.com/flightctl/flightctl/internal/rendered"
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

	log := log.InitLogs(cfg.RemoteAccessService.LogLevel)
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
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := tracerShutdown(shutdownCtx); err != nil {
			log.Errorf("failed to shut down tracer: %v", err)
		}
	}()

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	dataStore := store.NewStore(db, log.WithField("pkg", "store"))
	defer dataStore.Close()

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	processID := fmt.Sprintf("remote-access-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("failed connecting to Redis queue: %v", err)
	}
	defer func() {
		provider.Stop()
		provider.Wait()
	}()

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("initializing KV store: %v", err)
	}
	if err = rendered.Bus.Initialize(ctx, kvStore, provider, 0, log); err != nil {
		log.Fatalf("initializing rendered version bus: %v", err)
	}
	if err = rendered.Bus.Instance().Start(ctx); err != nil {
		log.Fatalf("starting rendered version bus: %v", err)
	}

	multiAuth, err := auth.InitMultiAuth(cfg, log, nil)
	if err != nil {
		log.Fatalf("initializing auth: %v", err)
	}

	server, err := remoteaccessserver.New(log, cfg, caClient, serverCerts, dataStore, rendered.Bus.Instance(), multiAuth)
	if err != nil {
		log.Fatalf("initializing remote-access server: %v", err)
	}

	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running remote-access server: %v", err)
	}
}
