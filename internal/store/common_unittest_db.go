package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func PrepareDBForUnitTests(ctx context.Context, log *logrus.Logger) (Store, *config.Config, string, *gorm.DB) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/store", "PrepareDBForUnitTests")
	defer span.End()

	cfg := config.NewDefault()
	dbTemp, sqlDb, err := InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	defer CloseDB(sqlDb)

	randomDBName := fmt.Sprintf("_%s", strings.ReplaceAll(uuid.New().String(), "-", "_"))
	log.Infof("DB name: %s", randomDBName)
	dbTemp = dbTemp.WithContext(ctx).Exec(fmt.Sprintf("CREATE DATABASE %s;", randomDBName))
	if dbTemp.Error != nil {
		log.Fatalf("creating database: %v", dbTemp.Error)
	}

	cfg.Database.Name = randomDBName
	db, sqlDb, err := InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	store := NewStore(db, sqlDb, log.WithField("pkg", "store"))
	if err := store.RunMigrations(ctx); err != nil {
		log.Fatalf("running migrations: %v", err)
	}
	return store, cfg, randomDBName, db
}

func DeleteTestDB(ctx context.Context, log *logrus.Logger, cfg *config.Config, store Store, dbName string) {
	err := store.Close()
	if err != nil {
		log.Fatalf("closing data store: %v", err)
	}
	cfg.Database.Name = "flightctl"
	db, sqlDb, err := InitDB(cfg, logrus.New())
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	defer CloseDB(sqlDb)
	db = db.WithContext(ctx).Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s;", dbName))
	if db.Error != nil {
		log.Fatalf("dropping database: %v", db.Error)
	}
}

func CloseDB(sqlDb *sql.DB) {
	_ = sqlDb.Close()
}
