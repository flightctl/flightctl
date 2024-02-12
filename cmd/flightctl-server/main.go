package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/queuejobs"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const (
	caCertValidityDays          = 365 * 10
	serverCertValidityDays      = 365 * 1
	clientBootStrapValidityDays = 365 * 1
	signerCertName              = "ca"
	serverCertName              = "server"
	clientBootstrapCertName     = "client-enrollment"
)

func main() {
	log := log.InitLogs()
	ctx := context.Background()

	log.Println("Starting device management service")
	defer log.Println("Device management service stopped")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}
	// TODO move into config
	// default certificate hostnames to localhost if nothing else is configured
	if len(cfg.Service.AltNames) == 0 {
		cfg.Service.AltNames = []string{"localhost"}
	}

	log.Printf("Using config: %s", cfg)

	log.Println("Initializing database")
	db, err := store.InitDB(cfg)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	defer store.CloseDB(db)

	dbPool := store.InitPgxPool(log, ctx, cfg)
	defer dbPool.Close()

	log.Println("Initializing data store")
	storeInst := store.NewStore(db, log.WithField("pkg", "store"))
	if err := storeInst.InitialMigration(); err != nil {
		log.Fatalf("running initial migration: %v", err)
	}

	log.Println("Initializing river queue")
	workers := river.NewWorkers()
	river.AddWorker(workers, queuejobs.NewRepoTester(log, db, storeInst))
	river.AddWorker(workers, queuejobs.NewFleetTemplateUpdateWorker(log, db, storeInst))

	var riverLogLevel = new(slog.LevelVar)
	riverLogLevel.Set(slog.LevelWarn)
	riverClient, err := river.NewClient(riverpgxv5.New(dbPool), &river.Config{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: riverLogLevel})),
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(1*time.Minute),
				func() (river.JobArgs, *river.InsertOpts) {
					return store.TestRepoArgs{}, nil
				},
				&river.PeriodicJobOpts{RunOnStart: true},
			),
		},
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 100},
		},
		Workers: workers,
	})
	if err != nil {
		log.Fatalf("failed creating river client: %v", err)
	}
	if err := riverClient.Start(ctx); err != nil {
		log.Fatalf("failed starting river client: %v", err)
	}
	defer func() {
		_ = riverClient.Stop(ctx)
	}()
	storeInst.SetRiverClient(dbPool, riverClient)

	// ensure the CA and server certificates are created and valid
	ca, _, err := crypto.EnsureCA(certFile(signerCertName), keyFile(signerCertName), "", signerCertName, caCertValidityDays)
	if err != nil {
		log.Fatalf("ensuring CA cert: %v", err)
	}
	serverCerts, _, err := ca.EnsureServerCertificate(certFile(serverCertName), keyFile(serverCertName), cfg.Service.AltNames, serverCertValidityDays)
	if err != nil {
		log.Fatalf("ensuring server cert: %v", err)
	}
	_, _, err = ca.EnsureClientCertificate(certFile(clientBootstrapCertName), keyFile(clientBootstrapCertName), clientBootstrapCertName, clientBootStrapValidityDays)
	if err != nil {
		log.Fatalf("ensuring bootstrap client cert: %v", err)
	}

	tlsConfig, err := crypto.TLSConfigForServer(ca.Config, serverCerts)
	if err != nil {
		log.Fatalf("failed creating TLS config: %v", err)
	}

	server := server.New(log, cfg, storeInst, db, tlsConfig)
	if err := server.Run(); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}

func certFile(name string) string {
	return filepath.Join(config.CertificateDir(), name+".crt")
}

func keyFile(name string) string {
	return filepath.Join(config.CertificateDir(), name+".key")
}
