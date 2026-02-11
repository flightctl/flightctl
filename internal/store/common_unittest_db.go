package store

import (
	"context"
	"os"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/store/testutil"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func PrepareDBForUnitTests(ctx context.Context, log *logrus.Logger) (Store, *config.Config, string, *gorm.DB) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/store", "PrepareDBForUnitTests")
	defer span.End()

	cfg, dbName, gormDb := testutil.CreateTestDB(ctx, log, "", InitDB)

	store := NewStore(gormDb, log.WithField("pkg", "store"))

	// Skip migrations when using template strategy - the template is already migrated
	strategy := os.Getenv("FLIGHTCTL_TEST_DB_STRATEGY")
	if strategy == testutil.StrategyTemplate {
		log.Debugf("Skipping local migrations - using pre-migrated template database")
		return store, cfg, dbName, gormDb
	}

	log.Debugf("Running local migrations on test database")
	if err := store.RunMigrations(ctx); err != nil {
		_ = store.Close()
		log.Fatalf("running local migrations: %v", err)
	}

	return store, cfg, dbName, gormDb
}

func DeleteTestDB(ctx context.Context, log *logrus.Logger, cfg *config.Config, store Store, dbName string) {
	if err := store.Close(); err != nil {
		log.Fatalf("closing data store: %v", err)
	}
	testutil.DeleteTestDB(ctx, log, cfg, nil, dbName, InitDB)
}

// CloseDB closes a gorm database connection
func CloseDB(db *gorm.DB) {
	testutil.CloseDB(db)
}
