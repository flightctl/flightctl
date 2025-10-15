package main

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/alert_exporter"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/org/resolvers"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

// ServiceConfig represents the external service configuration structure
type ServiceConfig struct {
	DB struct {
		External          string `yaml:"external"`
		Hostname          string `yaml:"hostname"`
		Port              int    `yaml:"port"`
		Name              string `yaml:"name"`
		User              string `yaml:"user"`
		UserPassword      string `yaml:"userPassword"`
		MigrationUser     string `yaml:"migrationUser"`
		MigrationPassword string `yaml:"migrationPassword"`
		SSLMode           string `yaml:"sslmode"`
		SSLCert           string `yaml:"sslcert"`
		SSLKey            string `yaml:"sslkey"`
		SSLRootCert       string `yaml:"sslrootcert"`
	} `yaml:"db"`
}

// loadExternalDatabaseConfig reads external database configuration if SERVICE_CONFIG_PATH is set
func loadExternalDatabaseConfig(cfg *config.Config, log *logrus.Logger) error {
	serviceConfigPath := os.Getenv("SERVICE_CONFIG_PATH")
	if serviceConfigPath == "" {
		log.Debug("SERVICE_CONFIG_PATH not set, using default database configuration")
		return nil
	}

	log.Infof("Reading external database configuration from: %s", serviceConfigPath)

	data, err := os.ReadFile(serviceConfigPath)
	if err != nil {
		return err
	}

	var serviceConfig ServiceConfig
	if err := yaml.Unmarshal(data, &serviceConfig); err != nil {
		return err
	}

	// Check if external database is enabled
	if serviceConfig.DB.External != "enabled" {
		log.Debug("External database not enabled, using default database configuration")
		return nil
	}

	log.Info("External database enabled, overriding database configuration")

	// Initialize database configuration if it doesn't exist
	if cfg.Database == nil {
		// Since dbConfig is not exported, we need to create a new config with defaults
		defaultCfg := config.NewDefault()
		cfg.Database = defaultCfg.Database
	}

	cfg.Database.Hostname = serviceConfig.DB.Hostname
	if serviceConfig.DB.Port < 0 || serviceConfig.DB.Port > 65535 {
		return fmt.Errorf("invalid port number: %d", serviceConfig.DB.Port)
	}
	//nolint:gosec
	cfg.Database.Port = uint(serviceConfig.DB.Port)
	cfg.Database.Name = serviceConfig.DB.Name
	cfg.Database.User = serviceConfig.DB.User
	cfg.Database.Password = config.SecureString(serviceConfig.DB.UserPassword)
	cfg.Database.MigrationUser = serviceConfig.DB.MigrationUser
	cfg.Database.MigrationPassword = config.SecureString(serviceConfig.DB.MigrationPassword)
	cfg.Database.SSLMode = serviceConfig.DB.SSLMode
	cfg.Database.SSLCert = serviceConfig.DB.SSLCert
	cfg.Database.SSLKey = serviceConfig.DB.SSLKey
	cfg.Database.SSLRootCert = serviceConfig.DB.SSLRootCert

	log.Infof("Using external database: %s@%s:%d/%s", cfg.Database.User, cfg.Database.Hostname, cfg.Database.Port, cfg.Database.Name)
	return nil
}

func main() {
	ctx := context.Background()

	log := log.InitLogs()
	log.Println("Starting alert exporter")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}

	// Load external database configuration if available
	if err := loadExternalDatabaseConfig(cfg, log); err != nil {
		log.Fatalf("loading external database configuration: %v", err)
	}

	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-alert-exporter")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-alert-exporter")

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	defer store.Close()

	processID := fmt.Sprintf("alert-exporter-%s-%s", util.GetHostname(), uuid.New().String())
	queuesProvider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("initializing queue provider: %v", err)
	}
	defer func() {
		queuesProvider.Stop()
		queuesProvider.Wait()
	}()

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("initializing kv store: %v", err)
	}
	defer kvStore.Close()

	publisher, err := worker_client.QueuePublisher(ctx, queuesProvider)
	if err != nil {
		log.Fatalf("initializing task queue publisher: %v", err)
	}
	defer publisher.Close()
	workerClient := worker_client.NewWorkerClient(publisher, log)

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	go orgCache.Start()
	defer orgCache.Stop()

	buildResolverOpts := resolvers.BuildResolverOptions{
		Config: cfg,
		Store:  store.Organization(),
		Log:    log,
		Cache:  orgCache,
	}

	if cfg.Auth != nil && cfg.Auth.AAP != nil {
		membershipCache := cache.NewMembershipTTL(cache.DefaultTTL)
		go membershipCache.Start()
		defer membershipCache.Stop()
		buildResolverOpts.MembershipCache = membershipCache
	}

	orgResolver, err := resolvers.BuildResolver(buildResolverOpts)
	if err != nil {
		log.Fatalf("failed to build organization resolver: %v", err)
	}
	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(store, workerClient, kvStore, nil, log, "", "", []string{}, orgResolver))

	server := alert_exporter.New(cfg, log)
	if err := server.Run(ctx, serviceHandler); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}
