package store

import (
	"fmt"

	"github.com/flightctl/flightctl/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"k8s.io/klog/v2"
)

func InitDB(cfg *config.Config) (*gorm.DB, error) {
	var dia gorm.Dialector

	if cfg.Database.Type == "pgsql" {
		dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d",
			cfg.Database.Hostname,
			cfg.Database.User,
			cfg.Database.Password,
			cfg.Database.Name,
			cfg.Database.Port,
		)
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
