package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
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
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// Helper functions for initialization

// initializeCertificates sets up CA and server certificates
func initializeCertificates(ctx context.Context, cfg *config.Config, log *logrus.Logger) (*crypto.CAClient, *crypto.TLSCertificateConfig, error) {
	ca, _, err := crypto.EnsureCA(cfg.CA)
	if err != nil {
		return nil, nil, err
	}

	var serverCerts *crypto.TLSCertificateConfig

	// check for user-provided certificate files
	if cfg.Service.SrvCertFile != "" || cfg.Service.SrvKeyFile != "" {
		if canReadCertAndKey, err := crypto.CanReadCertAndKey(cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile); !canReadCertAndKey {
			if err == nil {
				err = fmt.Errorf("failed to read server certificate/key from %q and %q", cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile)
			}
			return nil, nil, err
		}

		serverCerts, err = crypto.GetTLSCertificateConfig(cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile)
		if err != nil {
			return nil, nil, err
		}
	} else {
		srvCertFile := crypto.CertStorePath(cfg.Service.ServerCertName+".crt", cfg.Service.CertStore)
		srvKeyFile := crypto.CertStorePath(cfg.Service.ServerCertName+".key", cfg.Service.CertStore)

		// check if existing self-signed certificate is available
		if canReadCertAndKey, _ := crypto.CanReadCertAndKey(srvCertFile, srvKeyFile); canReadCertAndKey {
			serverCerts, err = crypto.GetTLSCertificateConfig(srvCertFile, srvKeyFile)
			if err != nil {
				return nil, nil, err
			}
		} else {
			// default to localhost if no alternative names are set
			if len(cfg.Service.AltNames) == 0 {
				cfg.Service.AltNames = []string{"localhost"}
			}

			serverCerts, err = ca.MakeAndWriteServerCertificate(ctx, srvCertFile, srvKeyFile, cfg.Service.AltNames, cfg.CA.ServerCertValidityDays)
			if err != nil {
				return nil, nil, err
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

	return ca, serverCerts, nil
}

// setupClientCertificates creates client certificates and config
func setupClientCertificates(ctx context.Context, ca *crypto.CAClient, cfg *config.Config) error {
	clientCertFile := crypto.CertStorePath(cfg.CA.ClientBootstrapCertName+".crt", cfg.Service.CertStore)
	clientKeyFile := crypto.CertStorePath(cfg.CA.ClientBootstrapCertName+".key", cfg.Service.CertStore)
	_, _, err := ca.EnsureClientCertificate(ctx, clientCertFile, clientKeyFile, cfg.CA.ClientBootstrapCommonName, cfg.CA.ClientBootstrapValidityDays)
	if err != nil {
		return err
	}

	// also write out a client config file
	caPemBytes, err := ca.GetCABundle()
	if err != nil {
		return err
	}

	err = client.WriteConfig(config.ClientConfigFile(), cfg.Service.BaseUrl, "", caPemBytes, nil)
	if err != nil {
		return err
	}

	return nil
}

// initializeStores sets up database and KV store
func initializeStores(ctx context.Context, cfg *config.Config, log *logrus.Logger) (store.Store, queues.Provider, kvstore.KVStore, error) {
	log.Info("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return nil, nil, nil, err
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))

	processID := fmt.Sprintf("api-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return nil, nil, nil, err
	}

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		return nil, nil, nil, err
	}

	if err = rendered.Bus.Initialize(ctx, kvStore, provider, time.Duration(cfg.Service.RenderedWaitTimeout), log); err != nil {
		return nil, nil, nil, err
	}

	return store, provider, kvStore, nil
}

// initializeMetricsCollectors sets up metrics collectors based on configuration
func initializeMetricsCollectors(ctx context.Context, cfg *config.Config, store store.Store, log *logrus.Logger) []prometheus.Collector {
	if cfg.Metrics == nil || !cfg.Metrics.Enabled {
		return nil
	}

	var collectors []prometheus.Collector

	if cfg.Metrics.DeviceCollector != nil && cfg.Metrics.DeviceCollector.Enabled {
		collectors = append(collectors, domain.NewDeviceCollector(ctx, store, log, cfg))
	}
	if cfg.Metrics.FleetCollector != nil && cfg.Metrics.FleetCollector.Enabled {
		collectors = append(collectors, domain.NewFleetCollector(ctx, store, log, cfg))
	}
	if cfg.Metrics.RepositoryCollector != nil && cfg.Metrics.RepositoryCollector.Enabled {
		collectors = append(collectors, domain.NewRepositoryCollector(ctx, store, log, cfg))
	}
	if cfg.Metrics.ResourceSyncCollector != nil && cfg.Metrics.ResourceSyncCollector.Enabled {
		collectors = append(collectors, domain.NewResourceSyncCollector(ctx, store, log, cfg))
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

	return collectors
}

// initializeServers creates API and agent servers
func initializeServers(ctx context.Context, cfg *config.Config, store store.Store, ca *crypto.CAClient, provider queues.Provider, tlsConfig, agentTlsConfig *tls.Config, log *logrus.Logger) (*apiserver.Server, *agentserver.AgentServer, error) {
	// create the agent service listener as tcp (combined HTTP+gRPC)
	agentListener, err := net.Listen("tcp", cfg.Service.AgentEndpointAddress)
	if err != nil {
		return nil, nil, err
	}

	agentServer, err := agentserver.New(ctx, log, cfg, store, ca, agentListener, provider, agentTlsConfig)
	if err != nil {
		return nil, nil, err
	}

	// Create API server listener and server
	listener, err := middleware.NewTLSListener(cfg.Service.Address, tlsConfig)
	if err != nil {
		return nil, nil, err
	}
	// we pass the grpc server for now, to let the console sessions to establish a connection in grpc
	apiServer := apiserver.New(log, cfg, store, ca, listener, provider, agentServer.GetGRPCServer())

	return apiServer, agentServer, nil
}

// startServers launches all servers in background goroutines
func startServers(apiServer *apiserver.Server, agentServer *agentserver.AgentServer, collectors []prometheus.Collector, serverCtx context.Context, cancel context.CancelFunc, errCh chan error, log *logrus.Logger) {
	// Start API server
	go func() {
		log.Info("Starting API server")
		err := apiServer.Run(serverCtx)

		// Always signal main thread
		select {
		case errCh <- err:
		default:
		}

		// Always cancel unless it was already a context cancellation
		if !errors.Is(err, context.Canceled) {
			cancel() // Cancel for both error AND success cases
		}

		// Log errors for debugging
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("API server error: %v", err)
		}
	}()

	// Start Agent server
	go func() {
		log.Info("Starting Agent server (gRPC)")
		err := agentServer.Run(serverCtx)

		// Always signal main thread
		select {
		case errCh <- err:
		default:
		}

		// Always cancel unless it was already a context cancellation
		if !errors.Is(err, context.Canceled) {
			cancel() // Cancel for both error AND success cases
		}

		// Log errors for debugging
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("Agent server error: %v", err)
		}
	}()

	// Start Metrics server if collectors are available
	if len(collectors) > 0 {
		go func() {
			log.Info("Starting Metrics server")
			metricsServer := metrics.NewMetricsServer(log, collectors...)
			err := metricsServer.Run(serverCtx)

			// Always signal main thread
			select {
			case errCh <- err:
			default:
			}

			// Always cancel unless it was already a context cancellation
			if !errors.Is(err, context.Canceled) {
				cancel() // Cancel for both error AND success cases
			}

			// Log errors for debugging
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Errorf("Metrics server error: %v", err)
			}
		}()
	}
}

func main() {
	log := log.InitLogs()

	if err := runCmd(log); err != nil {
		log.Fatalf("API service error: %v", err)
	}
}

func runCmd(log *logrus.Logger) error {
	log.Info("Starting API service")
	defer log.Info("API service stopped")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		return err
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	// Initialize certificates
	ca, serverCerts, err := initializeCertificates(ctx, cfg, log)
	if err != nil {
		return err
	}

	// Setup client certificates
	err = setupClientCertificates(ctx, ca, cfg)
	if err != nil {
		return err
	}

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-api")

	// Initialize stores
	store, provider, kvStore, err := initializeStores(ctx, cfg, log)
	if err != nil {
		return err
	}

	tlsConfig, agentTlsConfig, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	if err != nil {
		return err
	}
	if err = rendered.Bus.Instance().Start(ctx); err != nil {
		return err
	}

	// Initialize servers
	apiServer, agentServer, err := initializeServers(ctx, cfg, store, ca, provider, tlsConfig, agentTlsConfig, log)
	if err != nil {
		return err
	}

	// Start servers in background
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()

	// Set up cleanup function
	cleanupFunc := func(cleanupCtx context.Context) {
		log.Info("Starting cleanup...")

		// Cancel main context to stop rendered bus and other background services
		cancel()

		// Stop queue provider
		log.Info("Stopping queue provider")
		provider.Stop()
		provider.Wait()

		// Close KV store
		log.Info("Closing KV store connections")
		kvStore.Close()

		// Close database
		log.Info("Closing database connections")
		store.Close()

		// Shutdown tracer
		if tracerShutdown != nil {
			log.Info("Shutting down tracer")
			if err := tracerShutdown(cleanupCtx); err != nil {
				log.WithError(err).Error("Failed to shutdown tracer")
			}
		}

		log.Info("Cleanup completed")
	}

	// Initialize metrics collectors
	collectors := initializeMetricsCollectors(ctx, cfg, store, log)

	// Start all servers
	startServers(apiServer, agentServer, collectors, serverCtx, cancel, errCh, log)

	// Channel to coordinate shutdown completion
	shutdownComplete := make(chan struct{})

	// Set up graceful shutdown
	shutdown.GracefulShutdown(log, shutdownComplete, func(shutdownCtx context.Context) error {
		// Stop servers first
		serverCancel()
		return nil
	})

	log.Info("All servers started, waiting for shutdown signal...")

	// Wait for either error or shutdown completion
	var serverErr error
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("API service failed: %v", err)
			serverErr = err // Store error to return later
		}
	case <-shutdownComplete:
		// Graceful shutdown completed
	}

	// Single cleanup call for ALL exit paths
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanupCancel()
	cleanupFunc(cleanupCtx)

	log.Info("API service stopped, exiting...")
	return serverErr
}
