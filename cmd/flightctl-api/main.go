package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
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
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

func main() {
	logger := log.InitLogs()

	if err := runCmd(logger); err != nil {
		logger.Fatalf("API service error: %v", err)
	}
}

type apiConfig struct {
	cfg         *config.Config
	ca          *crypto.CAClient
	serverCerts *crypto.TLSCertificateConfig
}

func runCmd(log *logrus.Logger) error {
	log.Info("Starting API service")
	defer log.Info("API service stopped")

	// Create shutdown manager with explicit signals (SIGHUP removed) and timeout for metrics export
	shutdownMgr := shutdown.NewManager(log).
		WithSignals(syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT).
		WithTimeout(shutdown.DefaultShutdownTimeout)

	// Setup configuration
	ctx := context.Background()
	apiCfg, err := setupConfiguration(ctx, log)
	if err != nil {
		return err
	}

	// Setup services and register cleanup functions with shutdown manager
	services, err := setupServices(ctx, log, apiCfg, shutdownMgr)
	if err != nil {
		return err
	}

	// Setup servers and run with coordinated shutdown
	return setupAndRunServers(ctx, log, apiCfg, services, shutdownMgr)
}

type apiServices struct {
	store          store.Store
	ca             *crypto.CAClient
	provider       queues.Provider
	kvStore        kvstore.KVStore
	tlsConfig      *tls.Config
	agentTlsConfig *tls.Config
}

func setupConfiguration(ctx context.Context, log *logrus.Logger) (*apiConfig, error) {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		return nil, fmt.Errorf("reading configuration: %w", err)
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

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

func setupServices(ctx context.Context, log *logrus.Logger, apiCfg *apiConfig, shutdownMgr *shutdown.Manager) (*apiServices, error) {
	// Setup tracer with cleanup
	tracerShutdown := tracing.InitTracer(log, apiCfg.cfg, "flightctl-api")
	if tracerShutdown != nil {
		shutdownMgr.AddCleanup("tracer", func() error {
			log.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	// Initialize data store with cleanup
	log.Info("Initializing data store")
	db, err := store.InitDB(apiCfg.cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	shutdownMgr.AddCleanup("database", shutdown.DatabaseCloseFunc(log, store.Close))

	// Setup TLS configs
	tlsConfig, agentTlsConfig, err := crypto.TLSConfigForServer(apiCfg.ca.GetCABundleX509(), apiCfg.serverCerts)
	if err != nil {
		return nil, fmt.Errorf("failed creating TLS config: %w", err)
	}

	// Initialize queue provider with cleanup and force stop capability
	processID := fmt.Sprintf("api-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, apiCfg.cfg.KV.Hostname, apiCfg.cfg.KV.Port, apiCfg.cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return nil, fmt.Errorf("failed connecting to Redis queue: %w", err)
	}
	shutdownMgr.AddCleanup("queue-provider", shutdown.StopWaitFunc("queue-provider", provider.Stop, provider.Wait))

	// Set provider stop as force stop function for deadlock prevention
	shutdownMgr.WithForceStop(provider.Stop)

	// Initialize KV store with cleanup
	kvStore, err := kvstore.NewKVStore(ctx, log, apiCfg.cfg.KV.Hostname, apiCfg.cfg.KV.Port, apiCfg.cfg.KV.Password)
	if err != nil {
		return nil, fmt.Errorf("creating kvstore: %w", err)
	}
	shutdownMgr.AddCleanup("kv-store", shutdown.CloseFunc(kvStore.Close))

	// Initialize rendered bus
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

func setupAndRunServers(ctx context.Context, log *logrus.Logger, apiCfg *apiConfig, services *apiServices, shutdownMgr *shutdown.Manager) error {
	// Create the agent service listener as tcp (combined HTTP+gRPC)
	agentListener, err := net.Listen("tcp", apiCfg.cfg.Service.AgentEndpointAddress)
	if err != nil {
		return fmt.Errorf("creating agent listener: %w", err)
	}

	agentServer, err := agentserver.New(ctx, log, apiCfg.cfg, services.store, services.ca, agentListener, services.provider, services.agentTlsConfig)
	if err != nil {
		return fmt.Errorf("initializing agent server: %w", err)
	}

	// Create API server listener
	listener, err := middleware.NewTLSListener(apiCfg.cfg.Service.Address, services.tlsConfig)
	if err != nil {
		return fmt.Errorf("creating API listener: %w", err)
	}

	// Create API server - we pass the grpc server to let console sessions establish a connection
	server := apiserver.New(log, apiCfg.cfg, services.store, services.ca, listener, services.provider, agentServer.GetGRPCServer())

	// Add servers to the shutdown manager
	shutdownMgr.
		AddServer("API", shutdown.NewServerFunc(func(ctx context.Context) error {
			return server.Run(ctx)
		})).
		AddServer("agent", shutdown.NewServerFunc(func(ctx context.Context) error {
			return agentServer.Run(ctx)
		}))

	// Add metrics server if enabled
	if setupMetricsServerInShutdownMgr(ctx, log, apiCfg.cfg, services, shutdownMgr) {
		log.Info("Metrics server configured")
	}

	// Run with coordinated shutdown - all the complex error handling is now internal
	return shutdownMgr.Run(ctx)
}

func setupMetricsServerInShutdownMgr(ctx context.Context, log *logrus.Logger, cfg *config.Config, services *apiServices, shutdownMgr *shutdown.Manager) bool {
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
		shutdownMgr.AddServer("metrics", shutdown.MetricsServerFunc(func(ctx context.Context) error {
			return tracing.RunMetricsServer(ctx, log, cfg.Metrics.Address, collectors...)
		}))
		return true
	}
	return false
}
