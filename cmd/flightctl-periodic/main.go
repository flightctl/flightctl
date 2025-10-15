package main

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	periodic "github.com/flightctl/flightctl/internal/periodic_checker"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
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
	log.Println("Starting periodic")

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

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-periodic")
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

	server := periodic.New(cfg, log, store)
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}
