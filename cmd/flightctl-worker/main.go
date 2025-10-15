package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	workerserver "github.com/flightctl/flightctl/internal/worker_server"
	"github.com/flightctl/flightctl/pkg/k8sclient"
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
	log.Println("Starting worker service")
	defer log.Println("Worker service stopped")

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

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-worker")
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

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-worker")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-worker")

	processID := fmt.Sprintf("worker-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("failed connecting to Redis queue: %v", err)
	}

	k8sClient, err := k8sclient.NewK8SClient()
	if err != nil {
		log.WithError(err).Warning("initializing k8s client, assuming k8s is not supported")
		k8sClient = nil
	}

	// Initialize metrics collectors
	var workerCollector *worker.WorkerCollector
	if cfg.Metrics != nil && cfg.Metrics.Enabled {
		var collectors []metrics.NamedCollector
		if cfg.Metrics.WorkerCollector != nil && cfg.Metrics.WorkerCollector.Enabled {
			workerCollector = worker.NewWorkerCollector(ctx, log, cfg, provider)
			collectors = append(collectors, workerCollector)
		}

		if cfg.Metrics.SystemCollector != nil && cfg.Metrics.SystemCollector.Enabled {
			if systemMetricsCollector := metrics.NewSystemCollector(ctx, cfg); systemMetricsCollector != nil {
				collectors = append(collectors, systemMetricsCollector)
			}
		}

		if len(collectors) > 0 {
			go func() {
				metricsServer := instrumentation.NewMetricsServer(log, cfg, collectors...)
				if err := metricsServer.Run(ctx); err != nil {
					log.Errorf("Error running metrics server: %s", err)
				}
				cancel()
			}()
		}
	}

	server := workerserver.New(cfg, log, store, provider, k8sClient, workerCollector)
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
	cancel()
}
