package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"sigs.k8s.io/yaml"
)

// errDryRunComplete signals that migrations validated successfully in dry-run mode.
var errDryRunComplete = errors.New("dry-run complete")

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

	log.Infof("Using external database: %s@%s:%d/%s", cfg.Database.MigrationUser, cfg.Database.Hostname, cfg.Database.Port, cfg.Database.Name)
	return nil
}

func main() {
	log := log.InitLogs()

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.WithError(err).Fatal("reading configuration")
	}

	// Load external database configuration if available
	if err := loadExternalDatabaseConfig(cfg, log); err != nil {
		log.WithError(err).Fatal("loading external database configuration")
	}

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	dryRun := flag.Bool("dry-run", false, "Validate migrations without committing any changes")
	flag.Parse()

	ctx := context.Background()
	// Bypass span check for migration operations
	ctx = store.WithBypassSpanCheck(ctx)

	startMsg := "Starting Flight Control database migration"
	if *dryRun {
		startMsg += " in dry-run mode"
	}
	log.Info(startMsg)
	defer log.Info("Flight Control database migration completed")

	log.Infof("Using config: %s", cfg)

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-db-migrate")
	defer func() {
		if err = tracerShutdown(ctx); err != nil {
			log.WithError(err).Fatal("failed to shut down tracer")
		}
	}()

	log.Info("Initializing migration database connection")
	migrationDB, err := store.InitMigrationDB(cfg, log)
	if err != nil {
		log.WithError(err).Fatal("initializing migration database")
	}
	if log.IsLevelEnabled(logrus.DebugLevel) {
		migrationDB = migrationDB.Debug()
	}
	defer func() {
		if sqlDB, err := migrationDB.DB(); err != nil {
			log.WithError(err).Warn("failed to get database connection for cleanup")
		} else {
			if err := sqlDB.Close(); err != nil {
				log.WithError(err).Warn("failed to close database connection")
			}
		}
	}()

	if *dryRun {
		log.Info("Dry-run mode enabled: changes will be rolled back after validation")
	} else {
		log.Info("Running database migrations with migration user")
	}
	// Run all schema changes atomically so that a failure leaves the DB unchanged.
	if err = migrationDB.Transaction(func(tx *gorm.DB) error {
		// Create a temporary store bound to the transaction and run migrations
		if err = store.NewStore(tx, log.WithFields(logrus.Fields{
			"pkg":     "migration-store-tx",
			"dry_run": *dryRun,
		})).RunMigrations(ctx); err != nil {
			return err // rollback
		}
		if *dryRun {
			return errDryRunComplete // rollback but indicate success
		}
		return nil // commit
	}); err != nil {
		if errors.Is(err, errDryRunComplete) {
			log.Info("Dry-run completed successfully; no changes were committed.")
			return
		}
		log.WithError(err).Fatal("running database migrations")
	}

	log.Info("Database migration completed successfully")
}
