//go:build linux

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/pam_issuer_server"
	"github.com/flightctl/flightctl/pkg/log"
)

func main() {
	ctx := context.Background()

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().Fatalf("reading configuration: %v", err)
	}

	log := log.InitLogs(cfg.Service.LogLevel)
	log.Println("Starting PAM issuer service")
	defer log.Println("PAM issuer service stopped")
	log.Printf("Using config: %s", cfg)

	// Check if PAM OIDC issuer is configured
	if cfg.Auth == nil || cfg.Auth.PAMOIDCIssuer == nil {
		log.Fatalf("PAM OIDC issuer not configured")
	}

	ca, err := crypto.LoadInternalCA(cfg.CA)
	if err != nil {
		log.Fatalf("loading client-signer certificates: %v", err)
	}
	caClient := crypto.NewCAClient(cfg.CA, ca)

	serverCerts, err := config.LoadServerCertificates(cfg, log)
	if err != nil {
		log.Fatalf("loading server certificates: %v", err)
	}

	// Use separate configuration for PAM issuer service
	pamIssuerAddress := cfg.Auth.PAMOIDCIssuer.Address
	if pamIssuerAddress == "" {
		pamIssuerAddress = ":8444" // Default port for PAM issuer
	}

	tlsConfig, _, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	if err != nil {
		log.Fatalf("failed creating TLS config: %v", err)
	}

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-pam-issuer")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	go func() {
		listener, err := middleware.NewTLSListener(pamIssuerAddress, tlsConfig)
		if err != nil {
			log.Fatalf("creating listener: %s", err)
		}
		server := pam_issuer_server.New(log, cfg, caClient, listener)
		if err := server.Run(ctx); err != nil {
			log.Fatalf("Error running server: %s", err)
		}
		cancel()
	}()

	<-ctx.Done()
}
