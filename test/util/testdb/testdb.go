// Package testdb provides shared test database utilities
package testdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// InitDBFunc is a function that initializes a database connection
type InitDBFunc func(cfg *config.Config, log *logrus.Logger) (*gorm.DB, error)

// CreateTestDB creates a temporary test database by cloning from the migrated 'flightctl' database.
// IMPORTANT: Call integrationstack.EnsureRunning() in your test suite's BeforeSuite before using this.
// Returns the config, db name, and gorm.DB connection.
func CreateTestDB(ctx context.Context, log *logrus.Logger, prefix string, initDB InitDBFunc) (*config.Config, string, *gorm.DB) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/store/testutil", "CreateTestDB")
	defer span.End()

	cfg := config.NewDefault()
	ApplyIntegrationConnectionOverrides(cfg)
	randomDBName := generateRandomDBName(prefix)
	log.Debugf("Test DB name: %s", randomDBName)

	gormDb, err := cloneFromMigratedDB(ctx, cfg, randomDBName, log, initDB)
	if err != nil {
		log.Fatal(err)
	}

	cfg.Database.Name = randomDBName
	return cfg, randomDBName, gormDb
}

// CreateEmptyTestDB creates a completely empty database for testing migration scenarios.
// Unlike CreateTestDB which clones from the migrated 'flightctl' template, this creates a truly
// empty database with no schema. The caller must run any needed migrations/schema setup after creation.
// IMPORTANT: Call DeleteTestDB in your test's cleanup (e.g., AfterEach or defer) to drop the database.
// Returns the config, db name, and gorm.DB connection (same signature as CreateTestDB).
func CreateEmptyTestDB(ctx context.Context, log *logrus.Logger, prefix string, initDB InitDBFunc) (*config.Config, string, *gorm.DB) {
	cfg := config.NewDefault()
	ApplyIntegrationConnectionOverrides(cfg)
	dbName := generateRandomDBName(prefix)
	log.Debugf("Creating empty test DB: %s", dbName)

	// Create admin config with postgres admin credentials
	adminCfg := config.NewDefault()
	adminCfg.Database.Hostname = cfg.Database.Hostname
	adminCfg.Database.Port = cfg.Database.Port
	adminCfg.Database.Name = "postgres"
	adminCfg.Database.User = IntegrationPostgresAdminUser()
	adminCfg.Database.Password = IntegrationPostgresAdminPassword()

	// Use minimal admin connection for CREATE DATABASE (no template)
	adminDB, err := OpenMinimalAdminDB(adminCfg, log)
	if err != nil {
		log.Fatalf("admin connection for CREATE DATABASE: %v", err)
	}
	if err := adminDB.WithContext(ctx).Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, dbName)).Error; err != nil {
		CloseDB(adminDB)
		log.Fatalf("creating empty test db %s: %v", dbName, err)
	}
	CloseDB(adminDB)

	// Connect to new database as admin to set up permissions and extensions
	adminCfg.Database.Name = dbName
	adminDB, err = OpenMinimalAdminDB(adminCfg, log)
	if err != nil {
		log.Fatalf("admin connection for setup: %v", err)
	}

	// Create extensions that migrations need (app user lacks CREATE EXTENSION privilege)
	if err := adminDB.WithContext(ctx).Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`).Error; err != nil {
		CloseDB(adminDB)
		log.Fatalf("creating pg_trgm extension: %v", err)
	}

	// Grant schema permissions to app user (empty databases don't inherit grants from template)
	appUser := cfg.Database.User
	grants := []string{
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO "%s"`, appUser),
		fmt.Sprintf(`GRANT CREATE ON SCHEMA public TO "%s"`, appUser),
	}
	for _, grant := range grants {
		if err := adminDB.WithContext(ctx).Exec(grant).Error; err != nil {
			CloseDB(adminDB)
			log.Fatalf("granting schema permissions: %v", err)
		}
	}
	CloseDB(adminDB)

	// Connect to the new database using initDB for actual test operations
	cfg.Database.Name = dbName
	gormDb, err := initDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing empty test db %s: %v", dbName, err)
	}

	return cfg, dbName, gormDb
}

// OpenMinimalAdminDB creates a minimal GORM connection for admin operations (CREATE/DROP DATABASE).
// Unlike store.InitDB, this does NOT register prometheus metrics server or tracing plugins,
// making it suitable for temporary throwaway connections that only run DDL statements.
// Uses postgres admin credentials from IntegrationPostgresAdminUser/Password.
func OpenMinimalAdminDB(cfg *config.Config, log *logrus.Logger) (*gorm.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Database.Hostname,
		cfg.Database.Port,
		cfg.Database.User,
		cfg.Database.Password.Value(),
		cfg.Database.Name,
	)

	gormLogger := logger.New(log, logger.Config{
		LogLevel:             logger.Warn,
		ParameterizedQueries: true,
	})

	return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormLogger})
}

// cloneFromMigratedDB creates a new database by cloning from the already-migrated 'flightctl' database.
// Uses a minimal admin connection (postgres user) for CREATE DATABASE - no prometheus/tracing plugins.
// Returns a full-featured connection (via initDB) using the app user for actual test operations.
func cloneFromMigratedDB(ctx context.Context, cfg *config.Config, dbName string, log *logrus.Logger, initDB InitDBFunc) (*gorm.DB, error) {
	// Create minimal admin connection for CREATE DATABASE
	adminCfg := config.NewDefault()
	adminCfg.Database.Hostname = cfg.Database.Hostname
	adminCfg.Database.Port = cfg.Database.Port
	adminCfg.Database.Name = "postgres"
	adminCfg.Database.User = IntegrationPostgresAdminUser()
	adminCfg.Database.Password = IntegrationPostgresAdminPassword()

	adminDB, err := OpenMinimalAdminDB(adminCfg, log)
	if err != nil {
		return nil, fmt.Errorf("admin connection: %w", err)
	}
	defer CloseDB(adminDB)

	log.Debugf("Creating test database from template: flightctl")
	res := adminDB.WithContext(ctx).Exec(fmt.Sprintf("CREATE DATABASE %s TEMPLATE flightctl;", dbName))
	if res.Error != nil {
		return nil, fmt.Errorf("creating test db %s: %w", dbName, res.Error)
	}

	// Connect to the new database using initDB (with prometheus/tracing) for actual test operations
	cfg.Database.Name = dbName
	gormDb, err := initDB(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("initializing data store: %w", err)
	}

	return gormDb, nil
}

// DeleteTestDB drops the test database.
// Uses a minimal admin connection (postgres user) for DROP DATABASE - no prometheus/tracing plugins.
func DeleteTestDB(ctx context.Context, log *logrus.Logger, cfg *config.Config, db *gorm.DB, dbName string) {
	CloseDB(db)

	// Create minimal admin connection for DROP DATABASE
	adminCfg := config.NewDefault()
	adminCfg.Database.Hostname = cfg.Database.Hostname
	adminCfg.Database.Port = cfg.Database.Port
	adminCfg.Database.Name = "postgres"
	adminCfg.Database.User = IntegrationPostgresAdminUser()
	adminCfg.Database.Password = IntegrationPostgresAdminPassword()

	adminDB, err := OpenMinimalAdminDB(adminCfg, log)
	if err != nil {
		log.Fatalf("admin connection: %v", err)
	}
	defer CloseDB(adminDB)

	// Check for active connections before attempting to drop
	var activeConns []struct {
		Pid      int     `gorm:"column:pid"`
		Usename  string  `gorm:"column:usename"`
		State    string  `gorm:"column:state"`
		QueryAge string  `gorm:"column:query_age"`
		Query    *string `gorm:"column:query"`
	}
	adminDB.WithContext(ctx).Raw(fmt.Sprintf(`
		SELECT pid, usename, state, query,
		       COALESCE(now() - query_start, interval '0')::text AS query_age
		FROM pg_stat_activity
		WHERE datname = '%s' AND pid <> pg_backend_pid()`, dbName)).Scan(&activeConns)
	if len(activeConns) > 0 {
		log.Warnf("Found %d active connection(s) to %s before drop:", len(activeConns), dbName)
		for _, c := range activeConns {
			query := "<none>"
			if c.Query != nil {
				query = *c.Query
			}
			log.Warnf("  - pid=%d user=%s state=%s age=%s query=%s", c.Pid, c.Usename, c.State, c.QueryAge, query)
		}
	}

	// Terminate any remaining connections to the test database.
	// As postgres admin, we can terminate any connection.
	adminDB.WithContext(ctx).Exec(fmt.Sprintf(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid();",
		dbName))

	// Drop the test database
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
