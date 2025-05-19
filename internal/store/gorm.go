package store

import (
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/prometheus"
	"k8s.io/klog/v2"
)

func InitDB(cfg *config.Config, log *logrus.Logger) (*gorm.DB, error) {
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

	newLogger := logger.New(
		log,
		logger.Config{
			SlowThreshold:             time.Second, // Slow SQL threshold
			LogLevel:                  logger.Warn, // Log level
			IgnoreRecordNotFoundError: true,        // Ignore ErrRecordNotFound error for logger
			ParameterizedQueries:      true,        // Don't include params in the SQL log
			Colorful:                  false,       // Disable color
		},
	)

	newDB, err := gorm.Open(dia, &gorm.Config{Logger: newLogger, TranslateError: true})
	if err != nil {
		klog.Fatalf("failed to connect database: %v", err)
		return nil, err
	}

	// TODO: Make exposing DB metrics optional
	err = newDB.Use(prometheus.New(prometheus.Config{
		DBName:          cfg.Database.Name,
		RefreshInterval: 5,
		StartServer:     true,
		HTTPServerPort:  15691,
	}))

	if err != nil {
		klog.Fatalf("Failed to register prometheus exporter: %v", err)
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
