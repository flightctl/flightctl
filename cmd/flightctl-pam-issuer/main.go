//go:build linux

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/pam_issuer_server"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

func main() {
	ctx := context.Background()

	log := log.InitLogs()
	log.Println("Starting PAM issuer service")
	defer log.Println("PAM issuer service stopped")

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

	// Check if PAM OIDC issuer is configured
	if cfg.Auth == nil || cfg.Auth.PAMOIDCIssuer == nil {
		log.Fatalf("PAM OIDC issuer not configured")
	}

	ca, _, err := crypto.EnsureCA(cfg.CA)
	if err != nil {
		log.Fatalf("ensuring CA cert: %v", err)
	}

	var serverCerts *crypto.TLSCertificateConfig

	// Use separate configuration for PAM issuer service
	pamIssuerAddress := cfg.Auth.PAMOIDCIssuer.Address
	if pamIssuerAddress == "" {
		pamIssuerAddress = ":8444" // Default port for PAM issuer
	}

	// check for user-provided certificate files
	if cfg.Service.SrvCertFile != "" || cfg.Service.SrvKeyFile != "" {
		if canReadCertAndKey, err := crypto.CanReadCertAndKey(cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile); !canReadCertAndKey {
			log.Fatalf("cannot read provided server certificate or key: %v", err)
		}

		serverCerts, err = crypto.GetTLSCertificateConfig(cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile)
		if err != nil {
			log.Fatalf("failed to load provided certificate: %v", err)
		}
	} else {
		srvCertFile := crypto.CertStorePath("pam-issuer.crt", cfg.Service.CertStore)
		srvKeyFile := crypto.CertStorePath("pam-issuer.key", cfg.Service.CertStore)

		// check if existing self-signed certificate is available
		if canReadCertAndKey, _ := crypto.CanReadCertAndKey(srvCertFile, srvKeyFile); canReadCertAndKey {
			serverCerts, err = crypto.GetTLSCertificateConfig(srvCertFile, srvKeyFile)
			if err != nil {
				log.Fatalf("failed to load existing self-signed certificate: %v", err)
			}
		} else {
			// default to localhost if no alternative names are set
			altNames := cfg.Service.AltNames
			if len(altNames) == 0 {
				altNames = []string{"localhost"}
			}

			serverCerts, err = ca.MakeAndWriteServerCertificate(ctx, srvCertFile, srvKeyFile, altNames, cfg.CA.ServerCertValidityDays)
			if err != nil {
				log.Fatalf("failed to create self-signed certificate: %v", err)
			}
		}
	}

	// check for expired certificate
	for _, x509Cert := range serverCerts.Certs {
		expired := time.Now().After(x509Cert.NotAfter)
		log.Printf("checking certificate: subject='%s', issuer='%s', expiry='%v'",
			x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)

		if expired {
			log.Warnf("server certificate for '%s' issued by '%s' has expired on: %v",
				x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)
		}
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
		server := pam_issuer_server.New(log, cfg, ca, listener)
		if err := server.Run(ctx); err != nil {
			log.Fatalf("Error running server: %s", err)
		}
		cancel()
	}()

	<-ctx.Done()
}
