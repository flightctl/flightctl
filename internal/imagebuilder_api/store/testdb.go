package store

import (
	"context"
	"os"

	"github.com/flightctl/flightctl/internal/config/common"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	mainstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/testutil"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// PrepareDBForUnitTests creates a temporary test database for unit testing
func PrepareDBForUnitTests(ctx context.Context, log *logrus.Logger) (Store, *common.DatabaseConfig, string, *gorm.DB) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/imagebuilder_store", "PrepareDBForUnitTests")
	defer span.End()

	dbCfg, dbName, gormDb := testutil.CreateTestDB(ctx, log, "ib", mainstore.InitDB)

	store := NewStore(gormDb, log.WithField("pkg", "imagebuilder-store"))

	// Skip migrations when using template strategy - the template is already migrated
	strategy := os.Getenv("FLIGHTCTL_TEST_DB_STRATEGY")
	if strategy == testutil.StrategyTemplate {
		log.Debugf("Skipping local migrations - using pre-migrated template database")
		return store, dbCfg, dbName, gormDb
	}

	log.Debugf("Running local migrations on test database")
	if err := store.RunMigrations(ctx); err != nil {
		_ = store.Close()
		log.Fatalf("running local migrations: %v", err)
	}

	return store, dbCfg, dbName, gormDb
}

// DeleteTestDB drops the test database
func DeleteTestDB(ctx context.Context, log *logrus.Logger, dbCfg *common.DatabaseConfig, store Store, dbName string) {
	if err := store.Close(); err != nil {
		log.Fatalf("closing data store: %v", err)
	}
	testutil.DeleteTestDB(ctx, log, dbCfg, nil, dbName, mainstore.InitDB)
}
