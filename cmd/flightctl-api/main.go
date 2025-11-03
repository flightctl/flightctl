package main

import (
	"context"
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
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/org/resolvers"
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
	log := log.InitLogs()

	// Create shutdown manager for coordinated shutdown
	shutdownManager := shutdown.NewShutdownManager(log)

	// Create a context for fail-fast behavior
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Enable fail-fast behavior to maintain original server restart semantics
	shutdownManager.EnableFailFast(cancel)

	shutdown.HandleSignalsWithManager(log, shutdownManager, shutdown.DefaultGracefulShutdownTimeout)
	if err := runCmd(ctx, shutdownManager, log); err != nil {
		log.Fatalf("API service error: %v", err)
	}
}

//nolint:gocyclo
func runCmd(ctx context.Context, shutdownManager *shutdown.ShutdownManager, log *logrus.Logger) error {
	log.Info("Starting API service")
	defer log.Info("API service stopped")

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

	ca, _, err := crypto.EnsureCA(cfg.CA)
	if err != nil {
		return err
	}

	var serverCerts *crypto.TLSCertificateConfig

	// check for user-provided certificate files
	if cfg.Service.SrvCertFile != "" || cfg.Service.SrvKeyFile != "" {
		if canReadCertAndKey, err := crypto.CanReadCertAndKey(cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile); !canReadCertAndKey {
			return err
		}

		serverCerts, err = crypto.GetTLSCertificateConfig(cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile)
		if err != nil {
			return err
		}
	} else {
		srvCertFile := crypto.CertStorePath(cfg.Service.ServerCertName+".crt", cfg.Service.CertStore)
		srvKeyFile := crypto.CertStorePath(cfg.Service.ServerCertName+".key", cfg.Service.CertStore)

		// check if existing self-signed certificate is available
		if canReadCertAndKey, _ := crypto.CanReadCertAndKey(srvCertFile, srvKeyFile); canReadCertAndKey {
			serverCerts, err = crypto.GetTLSCertificateConfig(srvCertFile, srvKeyFile)
			if err != nil {
				return err
			}
		} else {
			// default to localhost if no alternative names are set
			if len(cfg.Service.AltNames) == 0 {
				cfg.Service.AltNames = []string{"localhost"}
			}

			serverCerts, err = ca.MakeAndWriteServerCertificate(ctx, srvCertFile, srvKeyFile, cfg.Service.AltNames, cfg.CA.ServerCertValidityDays)
			if err != nil {
				return err
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

	clientCertFile := crypto.CertStorePath(cfg.CA.ClientBootstrapCertName+".crt", cfg.Service.CertStore)
	clientKeyFile := crypto.CertStorePath(cfg.CA.ClientBootstrapCertName+".key", cfg.Service.CertStore)
	_, _, err = ca.EnsureClientCertificate(ctx, clientCertFile, clientKeyFile, cfg.CA.ClientBootstrapCommonName, cfg.CA.ClientBootstrapValidityDays)
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

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-api")

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return err
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))

	// Register database cleanup with high priority
	shutdownManager.Register("database", shutdown.PriorityLowest, shutdown.TimeoutDatabase, func(ctx context.Context) error {
		log.Info("Closing database connections")
		store.Close()
		return nil
	})

	tlsConfig, agentTlsConfig, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	if err != nil {
		return err
	}

	processID := fmt.Sprintf("api-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return err
	}

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		return err
	}

	// Register KV store cleanup
	shutdownManager.Register("kvstore", shutdown.PriorityLowest, shutdown.TimeoutStandard, func(ctx context.Context) error {
		log.Info("Closing KV store connections")
		kvStore.Close()
		return nil
	})

	// Register queue provider cleanup
	shutdownManager.Register("queues", shutdown.PriorityLowest, shutdown.TimeoutDatabase, func(ctx context.Context) error {
		log.Info("Stopping queue provider")
		provider.Stop()
		provider.Wait()
		return nil
	})
	if err = rendered.Bus.Initialize(ctx, kvStore, provider, time.Duration(cfg.Service.RenderedWaitTimeout), log); err != nil {
		return err
	}
	if err = rendered.Bus.Instance().Start(ctx); err != nil {
		return err
	}

	// create the agent service listener as tcp (combined HTTP+gRPC)
	agentListener, err := net.Listen("tcp", cfg.Service.AgentEndpointAddress)
	if err != nil {
		return err
	}

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	go orgCache.Start()

	// Register organization cache cleanup
	shutdownManager.Register("org-cache", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error {
		log.Info("Stopping organization cache")
		orgCache.Stop()
		return nil
	})

	buildResolverOpts := resolvers.BuildResolverOptions{
		Config: cfg,
		Store:  store.Organization(),
		Log:    log,
		Cache:  orgCache,
	}

	if cfg.Auth != nil && cfg.Auth.AAP != nil {
		membershipCache := cache.NewMembershipTTL(cache.DefaultTTL)
		go membershipCache.Start()

		// Register membership cache cleanup
		shutdownManager.Register("membership-cache", shutdown.PriorityLow, shutdown.TimeoutStandard, func(ctx context.Context) error {
			log.Info("Stopping membership cache")
			membershipCache.Stop()
			return nil
		})

		buildResolverOpts.MembershipCache = membershipCache
	}

	orgResolver, err := resolvers.BuildResolver(buildResolverOpts)
	if err != nil {
		return err
	}

	agentServer, err := agentserver.New(ctx, log, cfg, store, ca, agentListener, provider, agentTlsConfig, orgResolver)
	if err != nil {
		return err
	}

	// Create API server listener and server
	listener, err := middleware.NewTLSListener(cfg.Service.Address, tlsConfig)
	if err != nil {
		return err
	}
	// we pass the grpc server for now, to let the console sessions to establish a connection in grpc
	apiServer := apiserver.New(log, cfg, store, ca, listener, provider, agentServer.GetGRPCServer(), orgResolver)

	// Start servers in background and register for shutdown
	serverCtx, serverCancel := context.WithCancel(ctx)

	// Start API server
	go func() {
		log.Info("Starting API server")
		if err := apiServer.Run(serverCtx); err != nil {
			log.Errorf("API server error: %v", err)
			shutdownManager.TriggerFailFast("api-server", err)
		}
	}()

	// Start Agent server
	go func() {
		log.Info("Starting Agent server (gRPC)")
		if err := agentServer.Run(serverCtx); err != nil {
			log.Errorf("Agent server error: %v", err)
			shutdownManager.TriggerFailFast("agent-server", err)
		}
	}()

	// Register server shutdown - highest priority (graceful server stop)
	shutdownManager.Register("servers", shutdown.PriorityHighest, shutdown.TimeoutServer, func(ctx context.Context) error {
		log.Info("Gracefully stopping HTTP/gRPC servers")
		serverCancel()
		// TODO: Add graceful shutdown support to servers
		return nil
	})

	if cfg.Metrics != nil && cfg.Metrics.Enabled {
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
				defer func() {
					if err := systemMetricsCollector.Shutdown(); err != nil {
						log.Errorf("Failed to shutdown system metrics collector: %v", err)
					}
				}()
			}
		}
		if cfg.Metrics.HttpCollector != nil && cfg.Metrics.HttpCollector.Enabled {
			if httpMetricsCollector := metrics.NewHTTPMetricsCollector(ctx, cfg, "flightctl-api", log); httpMetricsCollector != nil {
				collectors = append(collectors, httpMetricsCollector)
				defer func() {
					if err := httpMetricsCollector.Shutdown(); err != nil {
						log.Errorf("Failed to shutdown HTTP metrics collector: %v", err)
					}
				}()
			}
		}

		// Start Metrics server
		go func() {
			log.Info("Starting Metrics server")
			metricsServer := metrics.NewMetricsServer(log, collectors...)
			if err := metricsServer.Run(serverCtx); err != nil {
				log.Errorf("Metrics server error: %v", err)
				shutdownManager.TriggerFailFast("metrics-server", err)
			}
		}()
	}

	// Register tracer shutdown
	shutdownManager.Register("tracer", shutdown.PriorityLowest, shutdown.TimeoutStandard, func(ctx context.Context) error {
		log.Info("Shutting down tracer")
		return tracerShutdown(ctx)
	})

	log.Info("All servers started, waiting for shutdown signal...")

	// Create a done channel that will be closed when shutdown is complete
	done := make(chan struct{})

	// Register a final shutdown callback that signals completion
	shutdownManager.Register("completion", shutdown.PriorityLast, shutdown.TimeoutCompletion, func(ctx context.Context) error {
		close(done)
		return nil
	})

	// Wait for either shutdown completion or fail-fast trigger
	select {
	case <-done:
		log.Info("All components shut down successfully")
		return nil
	case <-ctx.Done():
		// Fail-fast was triggered, wait a moment for shutdown to be handled by signal handler
		log.Info("Fail-fast shutdown triggered, waiting for coordinated shutdown...")
		<-done
		log.Info("Emergency shutdown completed")
		return fmt.Errorf("service failed and triggered emergency shutdown")
	}
}
