package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/config/common"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"
	"gorm.io/plugin/prometheus"
)

// InitDB initializes a database connection using the application user credentials.
func InitDB(dbCfg *common.DatabaseConfig, tracingCfg *common.TracingConfig, log *logrus.Logger) (*gorm.DB, error) {
	return initDBWithUser(dbCfg, tracingCfg, log, dbCfg.User, dbCfg.Password)
}

// InitMigrationDB initializes a database connection using migration user credentials.
func InitMigrationDB(dbCfg *common.DatabaseConfig, tracingCfg *common.TracingConfig, log *logrus.Logger) (*gorm.DB, error) {
	return initDBWithUser(dbCfg, tracingCfg, log, dbCfg.MigrationUser, dbCfg.MigrationPassword)
}

func initDBWithUser(dbCfg *common.DatabaseConfig, tracingCfg *common.TracingConfig, log *logrus.Logger, user string, password api.SecureString) (*gorm.DB, error) {
	var dia gorm.Dialector

	if dbCfg.Type != "pgsql" {
		errString := fmt.Sprintf("failed to connect database %s: only PostgreSQL is supported", dbCfg.Type)
		log.Fatal(errString)
		return nil, errors.New(errString)
	}
	dsn := dbCfg.CreateDSN(user, password)
	baseDia := postgres.Open(dsn)

	// Wrap the dialector to intercept error translation
	// postgres.Open returns *postgres.Dialector (pointer type)
	if pgDialector, ok := baseDia.(*postgres.Dialector); ok {
		dia = &constraintAwareDialector{Dialector: *pgDialector}
		log.Debug("Successfully wrapped postgres dialector with constraint-aware translator")
	} else {
		// Fallback if not postgres dialector (shouldn't happen)
		log.Warningf("Could not wrap dialector (type: %T), using default", baseDia)
		dia = baseDia
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

	// TranslateError: true will use our wrapped dialector's Translate method
	newDB, err := gorm.Open(dia, &gorm.Config{
		Logger:         newLogger,
		TranslateError: true,
	})
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
		return nil, err
	}

	// TODO: Make exposing DB metrics optional
	err = newDB.Use(prometheus.New(prometheus.Config{
		DBName:          dbCfg.Name,
		RefreshInterval: 5,
		StartServer:     true,
		HTTPServerPort:  15691,
	}))

	if err != nil {
		log.Fatalf("Failed to register prometheus exporter: %v", err)
		return nil, err
	}

	sqlDB, err := newDB.DB()
	if err != nil {
		log.Fatalf("failed to configure connections: %v", err)
		return nil, err
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)

	var serverVersion string
	if res := newDB.Raw("SHOW server_version").Scan(&serverVersion); res.Error != nil {
		log.Warningf("could not read PostgreSQL version (continuing): %v", res.Error)
	} else {
		log.Debugf("PostgreSQL server_version: %s", serverVersion)
	}

	if tracingCfg != nil && tracingCfg.Enabled {
		if err = newDB.Use(NewTraceContextEnforcer()); err != nil {
			log.Fatalf("failed to register OpenTelemetry GORM plugin: %v", err)
			return nil, err
		}
	}

	traceOpts := []tracing.Option{
		tracing.WithoutMetrics(),
	}

	if value := os.Getenv("GORM_TRACE_INCLUDE_QUERY_VARIABLES"); value == "" {
		traceOpts = append(traceOpts, tracing.WithoutQueryVariables())
	}

	if err = newDB.Use(tracing.NewPlugin(traceOpts...)); err != nil {
		log.Fatalf("failed to register OpenTelemetry GORM plugin: %v", err)
		return nil, err
	}

	return newDB, nil
}

type bypassSpanCheckKey struct{}

// WithBypassSpanCheck returns a child context that disables span enforcement.
func WithBypassSpanCheck(ctx context.Context) context.Context {
	return context.WithValue(ctx, bypassSpanCheckKey{}, true)
}

type traceContextEnforcer struct{}

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
			if !span.SpanContext().IsValid() {
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

// constraintAwareDialector wraps postgres.Dialector to intercept error translation
// By embedding postgres.Dialector, all its methods are promoted to constraintAwareDialector
// We only override Translate() to check for specific constraints
type constraintAwareDialector struct {
	postgres.Dialector
}

// Verify at compile-time that constraintAwareDialector implements gorm.Dialector
var _ gorm.Dialector = (*constraintAwareDialector)(nil)

// Translate intercepts PostgreSQL errors to check for specific authprovider constraints
func (d *constraintAwareDialector) Translate(err error) error {
	// Check for PostgreSQL-specific constraint violations first
	if pgErr, ok := err.(*pgconn.PgError); ok {

		// Check for specific authprovider constraints BEFORE default translation
		switch pgErr.ConstraintName {
		case ConstraintAuthProviderOIDCUnique:
			return flterrors.ErrDuplicateOIDCProvider
		case ConstraintAuthProviderOAuth2Unique:
			return flterrors.ErrDuplicateOAuth2Provider
		}
	}

	// Fall back to default postgres dialector translation for all other errors
	return d.Dialector.Translate(err)
}
