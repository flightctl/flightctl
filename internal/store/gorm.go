package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"
	"gorm.io/plugin/prometheus"
	"k8s.io/klog/v2"
)

func InitDB(cfg *config.Config, log *logrus.Logger) (*gorm.DB, error) {
	return initDBWithUser(cfg, log, cfg.Database.User, cfg.Database.Password)
}

func InitMigrationDB(cfg *config.Config, log *logrus.Logger) (*gorm.DB, error) {
	return initDBWithUser(cfg, log, cfg.Database.MigrationUser, cfg.Database.MigrationPassword)
}

func initDBWithUser(cfg *config.Config, log *logrus.Logger, user, password string) (*gorm.DB, error) {
	var dia gorm.Dialector

	if cfg.Database.Type == "pgsql" {
		dsn := fmt.Sprintf("host=%s user=%s password=%s port=%d",
			cfg.Database.Hostname,
			user,
			password,
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

	if err := newDB.Use(NewTraceContextEnforcer()); err != nil {
		klog.Fatalf("failed to register OpenTelemetry GORM plugin: %v", err)
		return nil, err
	}

	traceOpts := []tracing.Option{
		tracing.WithoutMetrics(),
	}

	if value := os.Getenv("GORM_TRACE_INCLUDE_QUERY_VARIABLES"); value == "" {
		traceOpts = append(traceOpts, tracing.WithoutQueryVariables())
	}

	if err := newDB.Use(tracing.NewPlugin(traceOpts...)); err != nil {
		klog.Fatalf("failed to register OpenTelemetry GORM plugin: %v", err)
		return nil, err
	}

	return newDB, nil
}

type bypassSpanCheckKey struct{}

// WithBypassSpanCheck returns a child context that disables span enforcement.
func WithBypassSpanCheck(ctx context.Context) context.Context {
	return context.WithValue(ctx, bypassSpanCheckKey{}, true)
}

type traceContextEnforcer struct {
}

func NewTraceContextEnforcer() gorm.Plugin {
	return &traceContextEnforcer{}
}

func (p *traceContextEnforcer) Name() string {
	return "traceContextEnforcer"
}

func (p *traceContextEnforcer) Initialize(db *gorm.DB) error {
	// Enforce context before each core DB operation
	if err := db.Callback().Query().Before("gorm:query").Register("traceContextEnforcer:query", p.enforce()); err != nil {
		return err
	}
	if err := db.Callback().Create().Before("gorm:create").Register("traceContextEnforcer:create", p.enforce()); err != nil {
		return err
	}
	if err := db.Callback().Update().Before("gorm:update").Register("traceContextEnforcer:update", p.enforce()); err != nil {
		return err
	}
	if err := db.Callback().Delete().Before("gorm:delete").Register("traceContextEnforcer:delete", p.enforce()); err != nil {
		return err
	}
	if err := db.Callback().Row().Before("gorm:row").Register("traceContextEnforcer:row", p.enforce()); err != nil {
		return err
	}
	if err := db.Callback().Raw().Before("gorm:raw").Register("traceContextEnforcer:raw", p.enforce()); err != nil {
		return err
	}
	return nil
}

func (p *traceContextEnforcer) enforce() func(tx *gorm.DB) {
	return func(tx *gorm.DB) {
		ctx := tx.Statement.Context
		if !isBypassSpanCheck(ctx) {
			span := trace.SpanFromContext(ctx)
			if !span.SpanContext().IsValid() && !isNoopSpan(span) {
				msg := "missing tracing span in GORM context"
				if value := os.Getenv("GORM_TRACE_ENFORCE_FATAL"); value != "" {
					debug.PrintStack()
					log.Fatalln(msg)
				} else {
					tx.Logger.Error(ctx, msg)
					_ = tx.AddError(errors.New(msg))
				}
			}
		}
	}
}

func isBypassSpanCheck(ctx context.Context) bool {
	val := ctx.Value(bypassSpanCheckKey{})
	bypass, _ := val.(bool)
	return bypass
}

// IsNoopSpan checks if the given span is a no-op span (e.g., from an uninitialized tracer).
func isNoopSpan(span trace.Span) bool {
	_, ok := span.(noop.Span)
	return ok
}
