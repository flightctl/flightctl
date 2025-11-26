//go:build linux

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/pam_issuer_server"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/sirupsen/logrus"
)

func main() {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("Failed to load configuration")
	}

	logger := log.InitLogs(cfg.Service.LogLevel)
	logger.Info("Starting PAM issuer service")
	defer logger.Info("PAM issuer service stopped")
	logger.Infof("Using config: %s", cfg)

	if err := runPAMIssuerService(cfg, logger); err != nil {
		logger.WithError(err).Fatal("PAM issuer service error")
	}
}

func runPAMIssuerService(cfg *config.Config, logger *logrus.Logger) error {
	ctx := context.Background()

	// Check if PAM OIDC issuer is configured
	if cfg.Auth == nil || cfg.Auth.PAMOIDCIssuer == nil {
		return fmt.Errorf("PAM OIDC issuer not configured")
	}

	ca, _, err := crypto.EnsureCA(cfg.CA)
	if err != nil {
		return fmt.Errorf("ensuring CA cert: %w", err)
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
			return fmt.Errorf("cannot read provided server certificate or key: %w", err)
		}

		serverCerts, err = crypto.GetTLSCertificateConfig(cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile)
		if err != nil {
			return fmt.Errorf("failed to load provided certificate: %w", err)
		}
	} else {
		srvCertFile := crypto.CertStorePath("pam-issuer.crt", cfg.Service.CertStore)
		srvKeyFile := crypto.CertStorePath("pam-issuer.key", cfg.Service.CertStore)

		// check if existing self-signed certificate is available
		if canReadCertAndKey, _ := crypto.CanReadCertAndKey(srvCertFile, srvKeyFile); canReadCertAndKey {
			serverCerts, err = crypto.GetTLSCertificateConfig(srvCertFile, srvKeyFile)
			if err != nil {
				return fmt.Errorf("failed to load existing self-signed certificate: %w", err)
			}
		} else {
			// default to localhost if no alternative names are set
			altNames := cfg.Service.AltNames
			if len(altNames) == 0 {
				altNames = []string{"localhost"}
			}

			serverCerts, err = ca.MakeAndWriteServerCertificate(ctx, srvCertFile, srvKeyFile, altNames, cfg.CA.ServerCertValidityDays)
			if err != nil {
				return fmt.Errorf("failed to create self-signed certificate: %w", err)
			}
		}
	}

	// check for expired certificate
	for _, x509Cert := range serverCerts.Certs {
		expired := time.Now().After(x509Cert.NotAfter)
		logger.Printf("checking certificate: subject='%s', issuer='%s', expiry='%v'",
			x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)

		if expired {
			logger.Warnf("server certificate for '%s' issued by '%s' has expired on: %v",
				x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)
		}
	}

	tlsConfig, _, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	if err != nil {
		return fmt.Errorf("failed creating TLS config: %w", err)
	}

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-pam-issuer")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracerShutdown(ctx); err != nil {
			logger.WithError(err).Error("Error shutting down tracer")
		}
	}()

	// Use single-server shutdown pattern
	config := shutdown.NewSingleServerConfig("PAM issuer", logger)
	return config.RunSingleServer(func(shutdownCtx context.Context) error {
		listener, err := middleware.NewTLSListener(pamIssuerAddress, tlsConfig)
		if err != nil {
			return fmt.Errorf("creating listener: %w", err)
		}

		server := pam_issuer_server.New(logger, cfg, ca, listener)
		if err := server.Run(shutdownCtx); err != nil {
			return fmt.Errorf("running PAM issuer server: %w", err)
		}
		return nil
	})
}
