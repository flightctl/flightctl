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

	// Simple context for initialization phase
	initCtx := context.Background()

	apiCfg, err := setupConfiguration(initCtx, cfg, logger)
	if err != nil {
		return err
	}

	// Setup services with direct defer statements
	tracerShutdown := tracing.InitTracer(logger, apiCfg.cfg, "flightctl-api")
	if tracerShutdown != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tracerShutdown(ctx); err != nil {
				logger.WithError(err).Error("Error shutting down tracer")
			}
		}()
	}

	logger.Info("Initializing data store")
	db, err := store.InitDB(apiCfg.cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, logger.WithField("pkg", "store"))
	defer store.Close()

	tlsConfig, agentTlsConfig, err := crypto.TLSConfigForServer(apiCfg.ca.GetCABundleX509(), apiCfg.serverCerts)
	if err != nil {
		return fmt.Errorf("failed creating TLS config: %w", err)
	}

	processID := fmt.Sprintf("api-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(initCtx, logger, processID, apiCfg.cfg.KV.Hostname, apiCfg.cfg.KV.Port, apiCfg.cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return fmt.Errorf("failed connecting to Redis queue: %w", err)
	}
	defer provider.Wait()
	defer provider.Stop()

	kvStore, err := kvstore.NewKVStore(initCtx, logger, apiCfg.cfg.KV.Hostname, apiCfg.cfg.KV.Port, apiCfg.cfg.KV.Password)
	if err != nil {
		return fmt.Errorf("creating kvstore: %w", err)
	}
	defer kvStore.Close()

	if err = rendered.Bus.Initialize(initCtx, kvStore, provider, time.Duration(apiCfg.cfg.Service.RenderedWaitTimeout), logger); err != nil {
		return fmt.Errorf("creating rendered version manager: %w", err)
	}
	if err = rendered.Bus.Instance().Start(initCtx); err != nil {
		return fmt.Errorf("starting rendered version manager: %w", err)
	}

	services := &apiServices{
		store:          store,
		ca:             apiCfg.ca,
		provider:       provider,
		kvStore:        kvStore,
		tlsConfig:      tlsConfig,
		agentTlsConfig: agentTlsConfig,
	}

	// Define servers to coordinate
	var servers []shutdown.ServerSpec

	// Always add API server and Agent server
	servers = append(servers,
		shutdown.ServerSpec{
			Name: "API server",
			Runner: func(shutdownCtx context.Context) error {
				// Create cancellable context to coordinate nested servers
				ctx, cancel := context.WithCancel(shutdownCtx)
				defer cancel()

				// Create the agent service listener as tcp (combined HTTP+gRPC)
				agentListener, err := net.Listen("tcp", apiCfg.cfg.Service.AgentEndpointAddress)
				if err != nil {
					return fmt.Errorf("creating agent listener: %w", err)
				}

				agentServer, err := agentserver.New(ctx, logger, apiCfg.cfg, services.store, services.ca, agentListener, services.provider, services.agentTlsConfig)
				if err != nil {
					return fmt.Errorf("initializing agent server: %w", err)
				}

				listener, err := middleware.NewTLSListener(apiCfg.cfg.Service.Address, services.tlsConfig)
				if err != nil {
					return fmt.Errorf("creating API listener: %w", err)
				}

				// We pass the grpc server for now, to let the console sessions to establish a connection in grpc
				server := apiserver.New(logger, apiCfg.cfg, services.store, services.ca, listener, services.provider, agentServer.GetGRPCServer())

				// Start both servers concurrently
				errCh := make(chan error, 2)

				go func() {
					if err := agentServer.Run(ctx); err != nil {
						errCh <- fmt.Errorf("agent server: %w", err)
					} else {
						errCh <- nil
					}
				}()

				go func() {
					if err := server.Run(ctx); err != nil {
						errCh <- fmt.Errorf("API server: %w", err)
					} else {
						errCh <- nil
					}
				}()

				// Wait for first error or first server completion
				var firstError error
				var remainingServers = 2

				for remainingServers > 0 {
					err := <-errCh
					remainingServers--

					// Record first non-context error and cancel context to stop sibling server
					if err != nil && !errors.Is(err, context.Canceled) && firstError == nil {
						firstError = err
						cancel() // Cancel context to stop the surviving server
					}
				}
				return firstError
			},
		},
	)

	// Create context for metrics collectors that can be cancelled during shutdown
	collectorsCtx, collectorsCancel := context.WithCancel(context.Background())
	services.collectorsCancel = collectorsCancel

	// Add metrics server if enabled
	setupMetricsServer(collectorsCtx, apiCfg.cfg, services, logger, &servers)

	// Use multi-server shutdown coordination with initCtx
	multiServerConfig := shutdown.NewMultiServerConfig("API service", logger)

	// Cancel collectors when the function exits (actual shutdown)
	defer func() {
		if services.collectorsCancel != nil {
			services.collectorsCancel()
		}
	}()
	return multiServerConfig.RunMultiServer(initCtx, servers)
}

type apiServices struct {
	store            store.Store
	ca               *crypto.CAClient
	provider         queues.Provider
	kvStore          kvstore.KVStore
	tlsConfig        *tls.Config
	agentTlsConfig   *tls.Config
	collectorsCancel context.CancelFunc // For cancelling metrics collectors during shutdown
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

// setupMetricsServer adds metrics server to the server list if metrics are enabled
func setupMetricsServer(ctx context.Context, cfg *config.Config, services *apiServices, logger *logrus.Logger, servers *[]shutdown.ServerSpec) {
	if cfg.Metrics == nil || !cfg.Metrics.Enabled {
		return
	}

	var collectors []prometheus.Collector
	if cfg.Metrics.DeviceCollector != nil && cfg.Metrics.DeviceCollector.Enabled {
		collectors = append(collectors, domain.NewDeviceCollector(ctx, services.store, logger, cfg))
	}
	if cfg.Metrics.FleetCollector != nil && cfg.Metrics.FleetCollector.Enabled {
		collectors = append(collectors, domain.NewFleetCollector(ctx, services.store, logger, cfg))
	}
	if cfg.Metrics.RepositoryCollector != nil && cfg.Metrics.RepositoryCollector.Enabled {
		collectors = append(collectors, domain.NewRepositoryCollector(ctx, services.store, logger, cfg))
	}
	if cfg.Metrics.ResourceSyncCollector != nil && cfg.Metrics.ResourceSyncCollector.Enabled {
		collectors = append(collectors, domain.NewResourceSyncCollector(ctx, services.store, logger, cfg))
	}
	if cfg.Metrics.SystemCollector != nil && cfg.Metrics.SystemCollector.Enabled {
		if systemMetricsCollector := metrics.NewSystemCollector(ctx, cfg); systemMetricsCollector != nil {
			collectors = append(collectors, systemMetricsCollector)
		}
	}
	if cfg.Metrics.HttpCollector != nil && cfg.Metrics.HttpCollector.Enabled {
		if httpMetricsCollector := metrics.NewHTTPMetricsCollector(ctx, cfg, "flightctl-api", logger); httpMetricsCollector != nil {
			collectors = append(collectors, httpMetricsCollector)
		}
	}

	if len(collectors) > 0 {
		*servers = append(*servers, shutdown.ServerSpec{
			Name:      "metrics server",
			IsMetrics: true, // Gets 60s grace period for shutdown metrics
			Runner: func(ctx context.Context) error {
				return tracing.RunMetricsServer(ctx, logger, cfg.Metrics.Address, collectors...)
			},
		})
	}
}
