package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	fleet_rollout "github.com/flightctl/flightctl/internal/tasks/fleet-rollout"
	"github.com/flightctl/flightctl/internal/tasks/repotester"
	"github.com/flightctl/flightctl/internal/tasks/resourcesync"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/thread"
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
	log.Println("Starting device management service")
	defer log.Println("Device management service stopped")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}
	log.Printf("Using config: %s", cfg)

	ca, _, err := crypto.EnsureCA(certFile(signerCertName), keyFile(signerCertName), "", signerCertName, caCertValidityDays)
	if err != nil {
		log.Fatalf("ensuring CA cert: %v", err)
	}

	// default certificate hostnames to localhost if nothing else is configured
	if len(cfg.Service.AltNames) == 0 {
		cfg.Service.AltNames = []string{"localhost"}
	}

	serverCerts, _, err := ca.EnsureServerCertificate(certFile(serverCertName), keyFile(serverCertName), cfg.Service.AltNames, serverCertValidityDays)
	if err != nil {
		log.Fatalf("ensuring server cert: %v", err)
	}
	_, _, err = ca.EnsureClientCertificate(certFile(clientBootstrapCertName), keyFile(clientBootstrapCertName), clientBootstrapCertName, clientBootStrapValidityDays)
	if err != nil {
		log.Fatalf("ensuring bootstrap client cert: %v", err)
	}

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	taskChannels := tasks.MakeTaskChannels()
	store := store.NewStore(db, taskChannels, log.WithField("pkg", "store"))
	defer store.Close()

	if err := store.InitialMigration(); err != nil {
		log.Fatalf("running initial migration: %v", err)
	}

	log.Println("Initializing async jobs")
	repoTester := repotester.NewRepoTester(log, store)
	repoTesterThread := thread.New(
		log.WithField("pkg", "repository-tester"), "Repository tester", threadIntervalMinute(2), repoTester.TestRepo)
	repoTesterThread.Start()
	defer repoTesterThread.Stop()

	ctx := context.Background()
	ctxWithCancel, cancelTasks := context.WithCancel(ctx)
	go fleet_rollout.FleetRollouts(ctxWithCancel, log, store, taskChannels)
	defer cancelTasks()

	resourceSync := resourcesync.NewResourceSync(log, store)
	resourceSyncThread := thread.New(
		log.WithField("pkg", "resourcesync"), "ResourceSync", threadIntervalMinute(2), resourceSync.Poll)
	resourceSyncThread.Start()
	defer resourceSyncThread.Stop()

	tlsConfig, err := crypto.TLSConfigForServer(ca.Config, serverCerts)
	if err != nil {
		log.Fatalf("failed creating TLS config: %v", err)
	}

	server := server.New(log, cfg, store, tlsConfig, ca)
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

func threadIntervalMinute(min float64) time.Duration {
	return time.Duration(min * float64(time.Minute))
}
