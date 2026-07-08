// Package migration provides database migration functionality for Flight Control.
// It runs schema migrations for both the main store and the imagebuilder store
// within a single transaction to ensure atomicity.
package migration

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/store"
	authproviderstore "github.com/flightctl/flightctl/internal/store/authprovider"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	certificatesigningrequeststore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	checkpointstore "github.com/flightctl/flightctl/internal/store/checkpoint"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	resourcesyncstore "github.com/flightctl/flightctl/internal/store/resourcesync"
	syncstatestore "github.com/flightctl/flightctl/internal/store/syncstate"
	templateversionstore "github.com/flightctl/flightctl/internal/store/templateversion"
	vulnerabilityfindingstore "github.com/flightctl/flightctl/internal/store/vulnerabilityfinding"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrDryRunComplete signals that migrations validated successfully in dry-run mode.
var ErrDryRunComplete = errors.New("dry-run complete")

// Run executes all database migrations within a single transaction.
// If dryRun is true, the transaction is rolled back after successful validation.
// The provided db must be connected as a user with migration privileges.
func Run(ctx context.Context, db *gorm.DB, log logrus.FieldLogger, dryRun bool) error {
	ctx = store.WithBypassSpanCheck(ctx)

	return db.Transaction(func(tx *gorm.DB) error {
		txLog := log.WithFields(logrus.Fields{
			"pkg":     "migration-store-tx",
			"dry_run": dryRun,
		})
		if err := runMainStoreMigrations(ctx, tx, txLog); err != nil {
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

// runMainStoreMigrations runs schema migrations for every resource's own store, in the same
// order the former monolithic DataStore.RunMigrations used, then applies the same
// post-migration customizations (FK-constraint drop, default-catalog backfill).
func runMainStoreMigrations(ctx context.Context, tx *gorm.DB, log logrus.FieldLogger) error {
	if err := tx.WithContext(ctx).AutoMigrate(&model.SchemaMigration{}); err != nil {
		return err
	}

	if err := devicestore.NewDeviceStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := enrollmentrequeststore.NewEnrollmentRequestStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := certificatesigningrequeststore.NewCertificateSigningRequestStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := fleetstore.NewFleetStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := templateversionstore.NewTemplateVersionStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := repositorystore.NewRepositoryStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := resourcesyncstore.NewResourceSyncStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := catalogstore.NewCatalogStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := eventstore.NewEventStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := checkpointstore.NewCheckpointStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := organizationstore.NewOrganizationStore(tx).InitialMigration(ctx); err != nil {
		return err
	}
	if err := authproviderstore.NewAuthProviderStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := vulnerabilityfindingstore.NewVulnerabilityFindingStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := syncstatestore.NewSyncStateStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}
	if err := dependencyrefstore.NewDependencyRefStore(tx, log).InitialMigration(ctx); err != nil {
		return err
	}

	return customizeMigration(ctx, tx)
}

// customizeMigration applies one-off schema fixups that don't belong to any single resource's
// own InitialMigration, ported verbatim from the former monolithic DataStore.customizeMigration.
func customizeMigration(ctx context.Context, tx *gorm.DB) error {
	db := tx.WithContext(ctx)

	if db.Migrator().HasConstraint("fleet_repos", "fk_fleet_repos_repository") {
		if err := db.Migrator().DropConstraint("fleet_repos", "fk_fleet_repos_repository"); err != nil {
			return err
		}
	}
	if db.Migrator().HasConstraint("device_repos", "fk_device_repos_repository") {
		if err := db.Migrator().DropConstraint("device_repos", "fk_device_repos_repository"); err != nil {
			return err
		}
	}

	return backfillDefaultCatalogs(ctx, tx)
}

// backfillDefaultCatalogs creates a default catalog for every organization that has no
// catalogs at all. This covers existing installations that pre-date catalog support.
// The migration key acts as a distributed once-only guard: concurrent replicas race on
// the unique primary key; the loser sees 0 RowsAffected and skips the backfill. Ported
// verbatim from the former monolithic DataStore.backfillDefaultCatalogs.
func backfillDefaultCatalogs(ctx context.Context, tx *gorm.DB) error {
	const migrationKey = "backfill_default_catalogs_v1"

	return tx.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&model.SchemaMigration{Key: migrationKey, AppliedAt: time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// Another replica already ran this migration.
			return nil
		}

		displayName := domain.DefaultCatalogDisplayName
		name := domain.DefaultCatalogName
		return tx.Exec(`
			INSERT INTO catalogs (org_id, name, spec, labels, annotations, created_at, updated_at)
			SELECT o.id, ?, ?, '{}'::jsonb, '{}'::jsonb, NOW(), NOW()
			FROM organizations o
			WHERE NOT EXISTS (
				SELECT 1 FROM catalogs c WHERE c.org_id = o.id AND c.owner IS NULL
			)`,
			name,
			model.MakeJSONField(domain.CatalogSpec{DisplayName: &displayName}),
		).Error
	})
}
