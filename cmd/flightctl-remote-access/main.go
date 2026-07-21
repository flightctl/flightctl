package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	instpprof "github.com/flightctl/flightctl/internal/instrumentation/pprof"
	"github.com/flightctl/flightctl/internal/instrumentation/profiling"
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

	if cfg.RemoteAccessService == nil {
		cfg.RemoteAccessService = config.NewDefaultRemoteAccessServiceConfig()
	}
	log := log.InitLogs(cfg.RemoteAccessService.LogLevel)
	log.Println("Starting remote-access service")
	defer log.Println("Remote-access service stopped")
	log.Printf("Using config: %s", cfg)

	caBundlePath := crypto.CertStorePath(cfg.CA.InternalConfig.CABundleFile, cfg.CA.InternalConfig.CertStore)
	caBundleCerts, err := crypto.LoadCACertsFromFile(caBundlePath)
	if err != nil {
		log.Fatalf("loading CA bundle: %v", err)
	}

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
	profiling.Start(ctx, log, cfg, "flightctl-remote-access", instpprof.DefaultPortRemoteAccess)

	if err := encryption.InitGlobalEncryption(log, cfg); err != nil {
		log.Fatalf("initializing encryption: %v", err)
	}

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

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

	server, err := remoteaccessserver.New(log, cfg, caBundleCerts, serverCerts, db, rendered.Bus.Instance())
	if err != nil {
		log.Fatalf("initializing remote-access server: %v", err)
	}

	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running remote-access server: %v", err)
	}
}
