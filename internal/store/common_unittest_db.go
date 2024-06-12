package store

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/google/uuid"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func PrepareDBForUnitTests(log *logrus.Logger) (Store, *config.Config, string, error) {
	cfg := config.NewDefault()
	cfg.Database.Name = ""
	dbTemp, err := InitDB(cfg, log)
	if err != nil {
		return nil, nil, "", fmt.Errorf("initializing data store: %w", err)
	}
	defer CloseDB(dbTemp)

	randomDBName := fmt.Sprintf("_%s", strings.ReplaceAll(uuid.New().String(), "-", "_"))
	log.Infof("DB name: %s", randomDBName)
	dbTemp = dbTemp.Exec(fmt.Sprintf("CREATE DATABASE %s;", randomDBName))
	if dbTemp.Error != nil {
		return nil, nil, "", fmt.Errorf("creating database: %w", dbTemp.Error)
	}

	cfg.Database.Name = randomDBName
	db, err := InitDB(cfg, log)
	if err != nil {
		return nil, nil, "", fmt.Errorf("initializing data store: %w", err)
	}

	store := NewStore(db, log.WithField("pkg", "store"))
	if err := store.InitialMigration(); err != nil {
		return nil, nil, "", fmt.Errorf("running initial migration: %w", err)
	}

	err = store.InitialMigration()
	if err != nil {
		return nil, nil, "", fmt.Errorf("running initial migration: %w", err)
	}

	return store, cfg, randomDBName, nil
}

func DeleteTestDB(cfg *config.Config, store Store, dbName string) {
	err := store.Close()
	Expect(err).ShouldNot(HaveOccurred())
	cfg.Database.Name = ""
	db, err := InitDB(cfg, logrus.New())
	Expect(err).ShouldNot(HaveOccurred())
	defer CloseDB(db)
	db = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s;", dbName))

	Expect(db.Error).ShouldNot(HaveOccurred())
}

func CloseDB(db *gorm.DB) {
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	_ = sqlDB.Close()
}
