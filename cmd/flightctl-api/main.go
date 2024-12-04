package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/api_server/agentserver"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)


func main() {
	log := log.InitLogs()
	log.Println("Starting API service")
	defer log.Println("API service stopped")

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

	ca, _, err := crypto.EnsureCA(cfg.Cryptography)
	if err != nil {
		log.Fatalf("ensuring CA cert: %v", err)
	}

	// default certificate hostnames to localhost if nothing else is configured
	if len(cfg.Service.AltNames) == 0 {
		cfg.Service.AltNames = []string{"localhost"}
	}

	serverCerts, _, err := ca.EnsureServerCertificate(crypto.CertFile(crypto.ServerCertName), crypto.KeyFile(crypto.ServerCertName), cfg.Service.AltNames, crypto.ServerCertValidityDays)
	if err != nil {
		log.Fatalf("ensuring server cert: %v", err)
	}

	_, _, err = ca.EnsureClientCertificate(crypto.CertFile(crypto.ClientBootstrapCertName), crypto.KeyFile(crypto.ClientBootstrapCertName), crypto.ClientBootstrapCommonName, crypto.ClientBootStrapValidityDays)
	if err != nil {
		log.Fatalf("ensuring bootstrap client cert: %v", err)
	}

	// also write out a client config file
	err = client.WriteConfig(config.ClientConfigFile(), cfg.Service.BaseUrl, "", ca.GetConfig(), nil)
	if err != nil {
		log.Fatalf("writing client config: %v", err)
	}

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	defer store.Close()

	if err := store.InitialMigration(); err != nil {
		log.Fatalf("running initial migration: %v", err)
	}

	tlsConfig, agentTlsConfig, grpcTlsConfig, err := crypto.TLSConfigForServer(ca.GetConfig(), serverCerts)
	if err != nil {
		log.Fatalf("failed creating TLS config: %v", err)
	}
	provider := queues.NewAmqpProvider(cfg.Queue.AmqpURL, log)

	metrics := instrumentation.NewApiMetrics(cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		listener, err := middleware.NewTLSListener(cfg.Service.Address, tlsConfig)
		if err != nil {
			log.Fatalf("creating listener: %s", err)
		}

		server := apiserver.New(log, cfg, store, ca, listener, provider, metrics)
		if err := server.Run(ctx); err != nil {
			log.Fatalf("Error running server: %s", err)
		}
		cancel()
	}()

	go func() {
		listener, err := middleware.NewTLSListener(cfg.Service.AgentEndpointAddress, agentTlsConfig)
		if err != nil {
			log.Fatalf("creating listener: %s", err)
		}

		agentserver := agentserver.New(log, cfg, store, ca, listener, metrics)
		if err := agentserver.Run(ctx); err != nil {
			log.Fatalf("Error running server: %s", err)
		}
		cancel()
	}()

	go func() {
		grpcServer := agentserver.NewAgentGrpcServer(log, cfg, grpcTlsConfig)
		if err := grpcServer.Run(ctx); err != nil {
			log.Fatalf("Error running server: %s", err)
		}
		cancel()
	}()

	if cfg.Prometheus != nil {
		go func() {
			metricsServer := instrumentation.NewMetricsServer(log, cfg, metrics)
			if err := metricsServer.Run(ctx); err != nil {
				log.Fatalf("Error running server: %s", err)
			}
			cancel()
		}()
	}

	<-ctx.Done()
}
