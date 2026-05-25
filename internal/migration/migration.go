// Package migration provides database migration functionality for Flight Control.
// It runs schema migrations for both the main store and the imagebuilder store
// within a single transaction to ensure atomicity.
package migration

import (
	"context"
	"errors"

	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// ErrDryRunComplete signals that migrations validated successfully in dry-run mode.
var ErrDryRunComplete = errors.New("dry-run complete")

// Run executes all database migrations within a single transaction.
// If dryRun is true, the transaction is rolled back after successful validation.
// The provided db must be connected as a user with migration privileges.
func Run(ctx context.Context, db *gorm.DB, log logrus.FieldLogger, dryRun bool) error {
	ctx = store.WithBypassSpanCheck(ctx)

	return db.Transaction(func(tx *gorm.DB) error {
		if err := store.NewStore(tx, log.WithFields(logrus.Fields{
			"pkg":     "migration-store-tx",
			"dry_run": dryRun,
		})).RunMigrations(ctx); err != nil {
			return err
		}

		if err := imagebuilderstore.NewStore(tx, log.WithFields(logrus.Fields{
			"pkg":     "imagebuild-migration-tx",
			"dry_run": dryRun,
		})).RunMigrations(ctx); err != nil {
			return err
		}

		if dryRun {
			return ErrDryRunComplete
		}
		return nil
	})
}
