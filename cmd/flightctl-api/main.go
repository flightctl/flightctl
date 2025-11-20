package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/api_server/agentserver"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/initialization"
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
	"github.com/spf13/cobra"
)

func main() {
	command := NewAPICommand()
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func NewAPICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flightctl-api [command]",
		Short: "Flight Control API server",
		Long:  "Flight Control API server",
		Run: func(cmd *cobra.Command, args []string) {
			// Default behavior: run serve command
			serveCmd := NewServeCommand()
			serveCmd.SetArgs(args)
			if err := serveCmd.Execute(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		},
	}

	cmd.AddCommand(NewInitCommand())
	cmd.AddCommand(NewServeCommand())

	return cmd
}

func NewInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize certificates",
		Long:  "Generate CA and server certificates without starting the API server",
		Run: func(cmd *cobra.Command, args []string) {
			if err := runInit(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		},
	}
}

func NewServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the API server",
		Long:  "Start the Flight Control API server",
		Run: func(cmd *cobra.Command, args []string) {
			if err := runServe(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		},
	}
}

func runInit() error {
	ctx := context.Background()

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		return fmt.Errorf("reading configuration: %w", err)
	}

	log := log.InitLogs(cfg.Service.LogLevel)
	log.Println("Initializing certificates")

	log.Printf("Using config: %s", cfg)
	ca, _, err := initialization.ServerCertificates(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("initializing certificates: %w", err)
	}

	err = initialization.BootstrapClientCertificates(ctx, cfg, ca)
	if err != nil {
		return fmt.Errorf("initializing bootstrap client certificates: %w", err)
	}

	log.Println("Successfully initialized certificates")
	return nil
}

//nolint:gocyclo
func runServe() error {
	ctx := context.Background()

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().Fatalf("reading configuration: %v", err)
	}

	log := log.InitLogs(cfg.Service.LogLevel)
	log.Println("Starting API service")
	defer log.Println("API service stopped")
	log.Printf("Using config: %s", cfg)

	// Initialize service and bootstrap client certificates
	// This functionality is also exposed via the service init subcommand, but keeping these here for backwards compatibility
	ca, serverCerts, err := initialization.ServerCertificates(ctx, cfg, log)
	if err != nil {
		log.Fatalf("initializing certificates: %v", err)
	}

	err = initialization.BootstrapClientCertificates(ctx, cfg, ca)
	if err != nil {
		log.Fatalf("initializing bootstrap client certificates: %v", err)
	}

	// Write out a client config file
	caPemBytes, err := ca.GetCABundle()
	if err != nil {
		log.Fatalf("loading CA certificate bundle: %v", err)
	}

	err = client.WriteConfig(config.ClientConfigFile(), cfg.Service.BaseUrl, "", caPemBytes, nil)
	if err != nil {
		log.Fatalf("writing client config: %v", err)
	}

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-api")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	defer store.Close()

	tlsConfig, agentTlsConfig, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	if err != nil {
		log.Fatalf("failed creating TLS config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)

	processID := fmt.Sprintf("api-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("failed connecting to Redis queue: %v", err)
	}

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("creating kvstore: %v", err)
	}
	if err = rendered.Bus.Initialize(ctx, kvStore, provider, time.Duration(cfg.Service.RenderedWaitTimeout), log); err != nil {
		log.Fatalf("creating rendered version manager: %v", err)
	}
	if err = rendered.Bus.Instance().Start(ctx); err != nil {
		log.Fatalf("starting rendered version manager: %v", err)
	}

	// create the agent service listener as tcp (combined HTTP+gRPC)
	agentListener, err := net.Listen("tcp", cfg.Service.AgentEndpointAddress)
	if err != nil {
		log.Fatalf("creating listener: %s", err)
	}

	agentServer, err := agentserver.New(ctx, log, cfg, store, ca, agentListener, provider, agentTlsConfig)
	if err != nil {
		log.Fatalf("initializing agent server: %v", err)
	}

	go func() {
		listener, err := middleware.NewTLSListener(cfg.Service.Address, tlsConfig)
		if err != nil {
			log.Fatalf("creating listener: %s", err)
		}
		// we pass the grpc server for now, to let the console sessions to establish a connection in grpc
		server := apiserver.New(log, cfg, store, ca, listener, provider, agentServer.GetGRPCServer())
		if err := server.Run(ctx); err != nil {
			log.Fatalf("Error running server: %s", err)
		}
		cancel()
	}()

	go func() {
		if err := agentServer.Run(ctx); err != nil {
			log.Fatalf("Error running server: %s", err)
		}
		cancel()
	}()

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

		go func() {
			if err := tracing.RunMetricsServer(ctx, log, cfg.Metrics.Address, collectors...); err != nil {
				log.Errorf("Error running metrics server: %s", err)
			}
			cancel()
		}()
	}

	<-ctx.Done()
	return nil
}
