package store

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sirupsen/logrus"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"k8s.io/klog/v2"
)

func InitDB(cfg *config.Config) (*gorm.DB, error) {
	var dia gorm.Dialector

	if cfg.Database.Type == "pgsql" {
		dsn := fmt.Sprintf("host=%s user=%s password=%s port=%d",
			cfg.Database.Hostname,
			cfg.Database.User,
			cfg.Database.Password,
			cfg.Database.Port,
		)
		if cfg.Database.Name != "" {
			dsn = fmt.Sprintf("%s dbname=%s", dsn, cfg.Database.Name)
		}
		dia = postgres.Open(dsn)
	} else {
		dia = sqlite.Open(cfg.Database.Name)
	}

	newDB, err := gorm.Open(dia, &gorm.Config{})
	if err != nil {
		klog.Fatalf("failed to connect database: %v", err)
		return nil, err
	}

	sqlDB, err := newDB.DB()
	if err != nil {
		klog.Fatalf("failed to configure connections: %v", err)
		return nil, err
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)

	if cfg.Database.Type == "pgsql" {
		var minorVersion string
		if result := newDB.Raw("SELECT version()").Scan(&minorVersion); result.Error != nil {
			klog.Infoln(result.Error.Error())
			return nil, result.Error
		}

		klog.Infof("PostgreSQL information: '%s'", minorVersion)
	}

	return newDB, nil
}

func InitPgxPool(log logrus.FieldLogger, ctx context.Context, cfg *config.Config) *pgxpool.Pool {
	dsn := fmt.Sprintf("host=%s user=%s password=%s port=%d",
		cfg.Database.Hostname,
		cfg.Database.User,
		cfg.Database.Password,
		cfg.Database.Port,
	)
	if cfg.Database.Name != "" {
		dsn = fmt.Sprintf("%s dbname=%s", dsn, cfg.Database.Name)
	}

	dbConn, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("failed parsing database config %s: %v", dsn, err)
	}
	dbPool, err := pgxpool.NewWithConfig(ctx, dbConn)
	if err != nil {
		log.Fatalf("failed creating DB pool: %v", err)
	}
	return dbPool
}
