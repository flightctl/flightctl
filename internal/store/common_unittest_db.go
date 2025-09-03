package store

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
	strategyLocal    = "local"
	strategyTemplate = "template"
)

func PrepareDBForUnitTests(ctx context.Context, log *logrus.Logger) (Store, *config.Config, string, *gorm.DB) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/store", "PrepareDBForUnitTests")
	defer span.End()

	cfg, dbName, db := CreateRandomTestDB(ctx, log)
	store := NewStore(db, log.WithField("pkg", "store"))
	return store, cfg, dbName, db
}

func CreateRandomTestDB(ctx context.Context, log *logrus.Logger) (*config.Config, string, *gorm.DB) {
	cfg := config.NewDefault()

	randomDBName := generateRandomDBName()
	log.Infof("DB name: %s", randomDBName)

	strategy := os.Getenv("FLIGHTCTL_TEST_DB_STRATEGY")
	if strategy == "" {
		strategy = strategyLocal
	}

	var (
		db  *gorm.DB
		err error
	)

	switch strategy {
	case strategyTemplate:
		db, err = setupTemplateStrategy(ctx, cfg, randomDBName, log)
		if err != nil {
			log.Fatal(err)
		}
	case strategyLocal:
		db, err = setupLocalStrategy(ctx, cfg, randomDBName, log)
		if err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown database initialization strategy: %s (valid: %s, %s)", strategy, strategyLocal, strategyTemplate)
	}

	return cfg, randomDBName, db
}

func DeleteTestDB(ctx context.Context, log *logrus.Logger, cfg *config.Config, store Store, dbName string) {
	err := store.Close()
	if err != nil {
		log.Fatalf("closing data store: %v", err)
	}
	cfg.Database.Name = "flightctl"
	db, err := InitDB(cfg, logrus.New())
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	defer CloseDB(db)
	db = db.WithContext(ctx).Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s;", dbName))
	if db.Error != nil {
		log.Fatalf("dropping database: %v", db.Error)
	}
}

func CloseDB(db *gorm.DB) {
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	_ = sqlDB.Close()
}

// Helpers

func generateRandomDBName() string {
	return fmt.Sprintf("_%s", strings.ReplaceAll(uuid.New().String(), "-", "_"))
}

func setupTemplateStrategy(ctx context.Context, cfg *config.Config, dbName string, log *logrus.Logger) (*gorm.DB, error) {
	originalName := cfg.Database.Name
	cfg.Database.Name = "postgres"
	adminDB, err := InitDB(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}
	defer CloseDB(adminDB)
	cfg.Database.Name = originalName

	templateDB := os.Getenv("FLIGHTCTL_TEST_TEMPLATE_DB")
	if templateDB == "" {
		templateDB = "flightctl_tmpl"
	}

	log.Infof("Creating test database from template: %s", templateDB)
	res := adminDB.WithContext(ctx).Exec(fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s;", dbName, templateDB))
	if res.Error != nil {
		return nil, fmt.Errorf("creating database from template: %w", res.Error)
	}

	cfg.Database.Name = dbName
	db, err := InitDB(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}

	return db, nil
}

func setupLocalStrategy(ctx context.Context, cfg *config.Config, dbName string, log *logrus.Logger) (*gorm.DB, error) {
	dbTemp, err := InitDB(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}
	defer CloseDB(dbTemp)

	log.Infof("Creating test database with local migrations")
	res := dbTemp.WithContext(ctx).Exec(fmt.Sprintf("CREATE DATABASE %s;", dbName))
	if res.Error != nil {
		return nil, fmt.Errorf("creating empty database: %w", res.Error)
	}

	cfg.Database.Name = dbName
	db, err := InitDB(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}

	log.Infof("Running local migrations on test database")
	store := NewStore(db, log.WithField("pkg", "store"))
	if err := store.RunMigrations(ctx); err != nil {
		return nil, fmt.Errorf("running local migrations: %w", err)
	}

	return db, nil
}
