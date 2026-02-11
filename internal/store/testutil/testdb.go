// Package testutil provides shared test database utilities
package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	StrategyLocal    = "local"
	StrategyTemplate = "template"
)

// InitDBFunc is a function that initializes a database connection
type InitDBFunc func(cfg *config.Config, log *logrus.Logger) (*gorm.DB, error)

// CreateTestDB creates a temporary test database and returns the config, db name, and gorm.DB connection.
// The caller is responsible for running migrations and creating the appropriate store.
func CreateTestDB(ctx context.Context, log *logrus.Logger, prefix string, initDB InitDBFunc) (*config.Config, string, *gorm.DB) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/store/testutil", "CreateTestDB")
	defer span.End()

	cfg := config.NewDefault()
	randomDBName := generateRandomDBName(prefix)
	log.Debugf("Test DB name: %s", randomDBName)

	strategy := os.Getenv("FLIGHTCTL_TEST_DB_STRATEGY")
	if strategy == "" {
		strategy = StrategyLocal
	}

	var (
		gormDb *gorm.DB
		err    error
	)

	switch strategy {
	case StrategyTemplate:
		gormDb, err = setupTemplateStrategy(ctx, cfg, randomDBName, log, initDB)
		if err != nil {
			log.Fatal(err)
		}
	case StrategyLocal:
		gormDb, err = setupLocalStrategy(ctx, cfg, randomDBName, log, initDB)
		if err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown database initialization strategy: %s (valid: %s, %s)", strategy, StrategyLocal, StrategyTemplate)
	}

	cfg.Database.Name = randomDBName
	return cfg, randomDBName, gormDb
}

// DeleteTestDB drops the test database
func DeleteTestDB(ctx context.Context, log *logrus.Logger, cfg *config.Config, db *gorm.DB, dbName string, initDB InitDBFunc) {
	CloseDB(db)

	cfg.Database.Name = "flightctl"
	adminDB, err := initDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	defer CloseDB(adminDB)

	adminDB = adminDB.WithContext(ctx).Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s;", dbName))
	if adminDB.Error != nil {
		log.Fatalf("dropping database: %v", adminDB.Error)
	}
}

// CloseDB closes the database connection
func CloseDB(db *gorm.DB) {
	if db == nil {
		return
	}
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	_ = sqlDB.Close()
}

func generateRandomDBName(prefix string) string {
	if prefix == "" {
		prefix = "test"
	}
	return fmt.Sprintf("_%s_%s", prefix, strings.ReplaceAll(uuid.New().String(), "-", "_"))
}

func setupTemplateStrategy(ctx context.Context, cfg *config.Config, dbName string, log *logrus.Logger, initDB InitDBFunc) (*gorm.DB, error) {
	originalName := cfg.Database.Name
	cfg.Database.Name = "postgres"
	adminDB, err := initDB(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}
	defer CloseDB(adminDB)
	cfg.Database.Name = originalName

	templateDB := os.Getenv("FLIGHTCTL_TEST_TEMPLATE_DB")
	if templateDB == "" {
		templateDB = "flightctl_tmpl"
	}

	log.Debugf("Creating test database from template: %s", templateDB)
	res := adminDB.WithContext(ctx).Exec(fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s;", dbName, templateDB))
	if res.Error != nil {
		return nil, fmt.Errorf("creating database from template: %w", res.Error)
	}

	cfg.Database.Name = dbName
	gormDb, err := initDB(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}

	return gormDb, nil
}

func setupLocalStrategy(ctx context.Context, cfg *config.Config, dbName string, log *logrus.Logger, initDB InitDBFunc) (*gorm.DB, error) {
	dbTemp, err := initDB(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}
	defer CloseDB(dbTemp)

	log.Debugf("Creating test database with local migrations")
	res := dbTemp.WithContext(ctx).Exec(fmt.Sprintf("CREATE DATABASE %s;", dbName))
	if res.Error != nil {
		return nil, fmt.Errorf("creating empty database: %w", res.Error)
	}

	cfg.Database.Name = dbName
	gormDb, err := initDB(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}

	return gormDb, nil
}
