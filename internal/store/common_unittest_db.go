package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func PrepareDBForUnitTests(ctx context.Context, log *logrus.Logger) (Store, *config.Config, string, *gorm.DB) {
	cfg := config.NewDefault()
	cfg.Database.Name = ""
	cfg.Database.Port = 8888

	dbTemp, err := InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	defer CloseDB(dbTemp)

	randomDBName := fmt.Sprintf("_%s", strings.ReplaceAll(uuid.New().String(), "-", "_"))
	log.Infof("DB name: %s", randomDBName)
	dbTemp = dbTemp.WithContext(WithBypassSpanCheck(ctx)).Exec(fmt.Sprintf("CREATE DATABASE %s;", randomDBName))
	if dbTemp.Error != nil {
		log.Fatalf("creating database: %v", dbTemp.Error)
	}

	cfg.Database.Name = randomDBName
	db, err := InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	db = db.WithContext(ctx)

	store := NewStore(db, log.WithField("pkg", "store"))
	if err := store.InitialMigration(context.Background()); err != nil {
		log.Fatalf("running initial migration: %v", err)
	}
	return store, cfg, randomDBName, db
}

func DeleteTestDB(ctx context.Context, log *logrus.Logger, cfg *config.Config, store Store, dbName string) {
	err := store.Close()
	if err != nil {
		log.Fatalf("closing data store: %v", err)
	}
	cfg.Database.Name = ""
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
