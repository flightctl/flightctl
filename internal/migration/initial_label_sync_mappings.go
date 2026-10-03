package migration

import (
	"context"
	"errors"
	"fmt"
	"time"

	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const initialLabelSyncMappingsMigrationKey = "seed_initial_label_sync_mappings_v1"

func seedInitialLabelSyncMappings(ctx context.Context, db *gorm.DB, filePath string, log logrus.FieldLogger) error {
	if log == nil {
		log = logrus.New()
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existingMigration model.SchemaMigration
		err := tx.Where("key = ?", initialLabelSyncMappingsMigrationKey).Take(&existingMigration).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("checking initial label-sync mappings migration: %w", err)
		}

		entries, err := labelsyncmappingservice.ReadInitialLabelSyncMappingEntries(filePath)
		if err != nil {
			log.WithError(err).WithField("file", filePath).Warn("Initial label-sync mappings were not attempted")
			return nil
		}

		orgs, err := organizationstore.NewOrganizationStore(tx).List(ctx, store.ListParams{})
		if err != nil {
			return fmt.Errorf("listing organizations for initial label-sync mappings: %w", err)
		}

		mappingStore := labelsyncmappingstore.NewStore(tx, log.WithField("pkg", "labelsyncmapping-store"))
		mappingService, err := labelsyncmappingservice.NewService(mappingStore, nil, nil, log)
		if err != nil {
			return fmt.Errorf("creating label-sync mapping service: %w", err)
		}

		seeder, err := labelsyncmappingservice.NewInitialLabelSyncMappingProvisioner(filePath, mappingService, log)
		if err != nil {
			log.WithError(err).WithField("file", filePath).Error("Failed to initialize initial label-sync mappings")
			return nil
		}

		result := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&model.SchemaMigration{Key: initialLabelSyncMappingsMigrationKey, AppliedAt: time.Now()})
		if result.Error != nil {
			return fmt.Errorf("recording initial label-sync mappings migration: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}

		orgIDs := make([]uuid.UUID, len(orgs))
		for i, org := range orgs {
			orgIDs[i] = org.ID
		}
		seeder.ProvisionEntriesForOrganizations(ctx, orgIDs, entries)
		return nil
	})
}
