package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os/signal"
	"syscall"
	"time"

	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/api_server/agentserver"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

func main() {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("reading configuration")
	}

	if err = runCmd(cfg); err != nil {
		log.InitLogs().WithError(err).Fatal("API service error")
	}
}

type apiConfig struct {
	cfg         *config.Config
	ca          *crypto.CAClient
	serverCerts *crypto.TLSCertificateConfig
}

func runCmd(cfg *config.Config) error {
	logger := log.InitLogs(cfg.Service.LogLevel)
	logger.Info("Starting API service")
	defer logger.Info("API service stopped")
	logger.Infof("Using config: %s", cfg)

	// Single context with signal handling - OS signal cancels context
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP)

	// Build cleanup functions incrementally as resources are created
	var cleanupFuncs []func() error
	defer func() {
		// First cancel context to signal all goroutines to stop
		logger.Info("Cancelling context to stop all servers")
		cancel()

		// Then run cleanup in reverse order after goroutines have stopped
		logger.Info("Starting cleanup")
		for i := len(cleanupFuncs) - 1; i >= 0; i-- {
			if err := cleanupFuncs[i](); err != nil {
				logger.WithError(err).Error("Cleanup error")
			}
		}
		logger.Info("Cleanup completed")
	}()

	apiCfg, err := setupConfiguration(ctx, cfg, logger)
	if err != nil {
		return err
	}

	// Setup services and collect cleanup functions
	services, err := setupServices(ctx, logger, apiCfg, &cleanupFuncs)
	if err != nil {
		return err
	}

	// Setup and run servers
	return runServers(ctx, cancel, logger, apiCfg, services)
}

type apiServices struct {
	store          store.Store
	ca             *crypto.CAClient
	provider       queues.Provider
	kvStore        kvstore.KVStore
	tlsConfig      *tls.Config
	agentTlsConfig *tls.Config
}

func setupConfiguration(ctx context.Context, cfg *config.Config, log *logrus.Logger) (*apiConfig, error) {
	ca, _, err := crypto.EnsureCA(cfg.CA)
	if err != nil {
		return nil, fmt.Errorf("ensuring CA cert: %w", err)
	}

	serverCerts, err := setupServerCertificates(ctx, cfg, ca, log)
	if err != nil {
		return nil, err
	}

	if err := setupClientCertificates(ctx, cfg, ca, log); err != nil {
		return nil, err
	}

	return &apiConfig{
		cfg:         cfg,
		ca:          ca,
		serverCerts: serverCerts,
	}, nil
}

func setupServerCertificates(ctx context.Context, cfg *config.Config, ca *crypto.CAClient, log *logrus.Logger) (*crypto.TLSCertificateConfig, error) {
	var serverCerts *crypto.TLSCertificateConfig
	var err error

	// check for user-provided certificate files
	if cfg.Service.SrvCertFile != "" || cfg.Service.SrvKeyFile != "" {
		if canReadCertAndKey, err := crypto.CanReadCertAndKey(cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile); !canReadCertAndKey {
			return nil, fmt.Errorf("cannot read provided server certificate or key: %w", err)
		}

		serverCerts, err = crypto.GetTLSCertificateConfig(cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load provided certificate: %w", err)
		}
	} else {
		srvCertFile := crypto.CertStorePath(cfg.Service.ServerCertName+".crt", cfg.Service.CertStore)
		srvKeyFile := crypto.CertStorePath(cfg.Service.ServerCertName+".key", cfg.Service.CertStore)

		// check if existing self-signed certificate is available
		if canReadCertAndKey, _ := crypto.CanReadCertAndKey(srvCertFile, srvKeyFile); canReadCertAndKey {
			serverCerts, err = crypto.GetTLSCertificateConfig(srvCertFile, srvKeyFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load existing self-signed certificate: %w", err)
			}
		} else {
			// default to localhost if no alternative names are set
			if len(cfg.Service.AltNames) == 0 {
				cfg.Service.AltNames = []string{"localhost"}
			}

			serverCerts, err = ca.MakeAndWriteServerCertificate(ctx, srvCertFile, srvKeyFile, cfg.Service.AltNames, cfg.CA.ServerCertValidityDays)
			if err != nil {
				return nil, fmt.Errorf("failed to create self-signed certificate: %w", err)
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

	return serverCerts, nil
}

func setupClientCertificates(ctx context.Context, cfg *config.Config, ca *crypto.CAClient, log *logrus.Logger) error {
	clientCertFile := crypto.CertStorePath(cfg.CA.ClientBootstrapCertName+".crt", cfg.Service.CertStore)
	clientKeyFile := crypto.CertStorePath(cfg.CA.ClientBootstrapCertName+".key", cfg.Service.CertStore)
	_, _, err := ca.EnsureClientCertificate(ctx, clientCertFile, clientKeyFile, cfg.CA.ClientBootstrapCommonName, cfg.CA.ClientBootstrapValidityDays)
	if err != nil {
		return fmt.Errorf("ensuring bootstrap client cert: %w", err)
	}

	// also write out a client config file
	caPemBytes, err := ca.GetCABundle()
	if err != nil {
		return fmt.Errorf("loading CA certificate bundle: %w", err)
	}

	err = client.WriteConfig(config.ClientConfigFile(), cfg.Service.BaseUrl, "", caPemBytes, nil)
	if err != nil {
		return fmt.Errorf("writing client config: %w", err)
	}

	return nil
}

func setupServices(ctx context.Context, log *logrus.Logger, apiCfg *apiConfig, cleanupFuncs *[]func() error) (*apiServices, error) {
	tracerShutdown := tracing.InitTracer(log, apiCfg.cfg, "flightctl-api")
	if tracerShutdown != nil {
		*cleanupFuncs = append(*cleanupFuncs, func() error {
			log.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	log.Info("Initializing data store")
	db, err := store.InitDB(apiCfg.cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	*cleanupFuncs = append(*cleanupFuncs, func() error {
		log.Info("Closing database connections")
		return store.Close()
	})

	tlsConfig, agentTlsConfig, err := crypto.TLSConfigForServer(apiCfg.ca.GetCABundleX509(), apiCfg.serverCerts)
	if err != nil {
		return nil, fmt.Errorf("failed creating TLS config: %w", err)
	}

	processID := fmt.Sprintf("api-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, apiCfg.cfg.KV.Hostname, apiCfg.cfg.KV.Port, apiCfg.cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return nil, fmt.Errorf("failed connecting to Redis queue: %w", err)
	}
	*cleanupFuncs = append(*cleanupFuncs, func() error {
		log.Info("Stopping queue provider")
		provider.Stop()
		provider.Wait()
		return nil
	})

	kvStore, err := kvstore.NewKVStore(ctx, log, apiCfg.cfg.KV.Hostname, apiCfg.cfg.KV.Port, apiCfg.cfg.KV.Password)
	if err != nil {
		return nil, fmt.Errorf("creating kvstore: %w", err)
	}
	*cleanupFuncs = append(*cleanupFuncs, func() error {
		log.Info("Closing KV store connections")
		kvStore.Close()
		return nil
	})

	if err = rendered.Bus.Initialize(ctx, kvStore, provider, time.Duration(apiCfg.cfg.Service.RenderedWaitTimeout), log); err != nil {
		return nil, fmt.Errorf("creating rendered version manager: %w", err)
	}
	if err = rendered.Bus.Instance().Start(ctx); err != nil {
		return nil, fmt.Errorf("starting rendered version manager: %w", err)
	}

	return &apiServices{
		store:          store,
		ca:             apiCfg.ca,
		provider:       provider,
		kvStore:        kvStore,
		tlsConfig:      tlsConfig,
		agentTlsConfig: agentTlsConfig,
	}, nil
}

func runServers(ctx context.Context, cancel context.CancelFunc, log *logrus.Logger, apiCfg *apiConfig, services *apiServices) error {
	// create the agent service listener as tcp (combined HTTP+gRPC)
	agentListener, err := net.Listen("tcp", apiCfg.cfg.Service.AgentEndpointAddress)
	if err != nil {
		return fmt.Errorf("creating listener: %w", err)
	}

	agentServer, err := agentserver.New(ctx, log, apiCfg.cfg, services.store, services.ca, agentListener, services.provider, services.agentTlsConfig)
	if err != nil {
		return fmt.Errorf("initializing agent server: %w", err)
	}

	listener, err := middleware.NewTLSListener(apiCfg.cfg.Service.Address, services.tlsConfig)
	if err != nil {
		return fmt.Errorf("creating listener: %w", err)
	}

	// we pass the grpc server for now, to let the console sessions to establish a connection in grpc
	server := apiserver.New(log, apiCfg.cfg, services.store, services.ca, listener, services.provider, agentServer.GetGRPCServer())

	// Start servers in goroutines, collect errors
	errCh := make(chan error, 3)

	go func() {
		log.Info("Starting API server")
		if err := server.Run(ctx); err != nil {
			errCh <- fmt.Errorf("API server: %w", err)
		} else {
			errCh <- nil
		}
	}()

	go func() {
		log.Info("Starting agent server")
		if err := agentServer.Run(ctx); err != nil {
			errCh <- fmt.Errorf("agent server: %w", err)
		} else {
			errCh <- nil
		}
	}()

	// Track number of servers started (always start API + agent, optionally metrics)
	serversStarted := 2
	if setupMetricsServer(ctx, log, apiCfg.cfg, services, errCh) {
		serversStarted++
	}

	// Wait for all servers to complete before returning
	log.Info("API service started, waiting for shutdown signal...")
	var firstError error
	for i := 0; i < serversStarted; i++ {
		if err := <-errCh; err != nil {
			if firstError == nil {
				firstError = err
				// Cancel context on first error to trigger shutdown of all other servers
				if !errors.Is(err, context.Canceled) {
					log.Info("Triggering shutdown of all servers due to error")
					cancel()
					// Force provider shutdown to unblock any servers from provider.Wait()
					services.provider.Stop()
				}
			}
			log.WithError(err).Error("Server stopped with error")
		}
	}

	// Handle shutdown reason
	if errors.Is(firstError, context.Canceled) {
		log.Info("Servers stopped due to shutdown signal")
		return nil // Normal shutdown
	} else if firstError != nil {
		return firstError // Error shutdown
	}

	log.Info("Servers stopped normally")
	return nil // Normal completion
}

func setupMetricsServer(ctx context.Context, log *logrus.Logger, cfg *config.Config, services *apiServices, errCh chan error) bool {
	if cfg.Metrics == nil || !cfg.Metrics.Enabled {
		return false
	}

	var collectors []prometheus.Collector
	if cfg.Metrics.DeviceCollector != nil && cfg.Metrics.DeviceCollector.Enabled {
		collectors = append(collectors, domain.NewDeviceCollector(ctx, services.store, log, cfg))
	}
	if cfg.Metrics.FleetCollector != nil && cfg.Metrics.FleetCollector.Enabled {
		collectors = append(collectors, domain.NewFleetCollector(ctx, services.store, log, cfg))
	}
	if cfg.Metrics.RepositoryCollector != nil && cfg.Metrics.RepositoryCollector.Enabled {
		collectors = append(collectors, domain.NewRepositoryCollector(ctx, services.store, log, cfg))
	}
	if cfg.Metrics.ResourceSyncCollector != nil && cfg.Metrics.ResourceSyncCollector.Enabled {
		collectors = append(collectors, domain.NewResourceSyncCollector(ctx, services.store, log, cfg))
	}
	if cfg.Metrics.SystemCollector != nil && cfg.Metrics.SystemCollector.Enabled {
		if systemMetricsCollector := metrics.NewSystemCollector(ctx, cfg); systemMetricsCollector != nil {
			collectors = append(collectors, systemMetricsCollector)
		}
	}
	if cfg.Metrics.HttpCollector != nil && cfg.Metrics.HttpCollector.Enabled {
		if httpMetricsCollector := metrics.NewHTTPMetricsCollector(ctx, cfg, "flightctl-api", log); httpMetricsCollector != nil {
			collectors = append(collectors, httpMetricsCollector)
		}
	}

	if len(collectors) > 0 {
		go func() {
			log.Info("Starting metrics server")
			if err := tracing.RunMetricsServer(ctx, log, cfg.Metrics.Address, collectors...); err != nil {
				errCh <- fmt.Errorf("metrics server: %w", err)
			} else {
				errCh <- nil
			}
		}()
		return true
	}
	return false
}
