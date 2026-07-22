package syncstate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store interface {
	InitialMigration(ctx context.Context) error
	Get(ctx context.Context, orgID uuid.UUID, resourceKey string) (*model.SyncState, error)
	Set(ctx context.Context, orgID uuid.UUID, state *model.SyncState) error
	SetLastCheckedAt(ctx context.Context, orgID uuid.UUID, resourceKey string, t time.Time) error
	BulkUpsert(ctx context.Context, orgID uuid.UUID, states []model.SyncState) error
	BulkUpdateLastCheckedAt(ctx context.Context, orgID uuid.UUID, resourceKeys []string, t time.Time) error
}

type SyncStateStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

// Make sure we conform to the Store interface
var _ Store = (*SyncStateStore)(nil)

func NewSyncStateStore(db *gorm.DB, log logrus.FieldLogger) Store {
	return &SyncStateStore{dbHandler: db, log: log}
}

func (s *SyncStateStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *SyncStateStore) InitialMigration(ctx context.Context) error {
	return s.getDB(ctx).AutoMigrate(&model.SyncState{})
}

func (s *SyncStateStore) Get(ctx context.Context, orgID uuid.UUID, resourceKey string) (*model.SyncState, error) {
	var state model.SyncState
	result := s.getDB(ctx).Where("org_id = ? AND resource_key = ?", orgID, resourceKey).Take(&state)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, store.ErrorFromGormError(result.Error)
	}
	return &state, nil
}

func (s *SyncStateStore) Set(ctx context.Context, orgID uuid.UUID, state *model.SyncState) error {
	if state == nil {
		return fmt.Errorf("cannot set nil SyncState")
	}
	state.OrgID = orgID
	result := s.getDB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "org_id"}, {Name: "resource_key"}},
		UpdateAll: true,
	}).Create(state)
	if result.Error != nil {
		return store.ErrorFromGormError(result.Error)
	}
	return nil
}

func (s *SyncStateStore) SetLastCheckedAt(ctx context.Context, orgID uuid.UUID, resourceKey string, t time.Time) error {
	result := s.getDB(ctx).Model(&model.SyncState{}).
		Where("org_id = ? AND resource_key = ?", orgID, resourceKey).
		Update("last_checked_at", t)
	if result.Error != nil {
		return store.ErrorFromGormError(result.Error)
	}
	return nil
}

// BulkUpsert inserts or updates multiple sync state rows in a single batch.
func (s *SyncStateStore) BulkUpsert(ctx context.Context, orgID uuid.UUID, states []model.SyncState) error {
	if len(states) == 0 {
		return nil
	}
	for i := range states {
		states[i].OrgID = orgID
	}
	return s.getDB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "org_id"}, {Name: "resource_key"}},
		UpdateAll: true,
	}).Create(&states).Error
}

// BulkUpdateLastCheckedAt updates last_checked_at for all given resource keys
// in a single statement. It also resets probe_status to "Synced" and clears
// probe_message so that transient probe failures don't persist across
// successful unchanged polls.
func (s *SyncStateStore) BulkUpdateLastCheckedAt(ctx context.Context, orgID uuid.UUID, resourceKeys []string, t time.Time) error {
	if len(resourceKeys) == 0 {
		return nil
	}
	return s.getDB(ctx).Model(&model.SyncState{}).
		Where("org_id = ? AND resource_key IN ?", orgID, resourceKeys).
		Updates(map[string]interface{}{
			"last_checked_at": t,
			"probe_status":    "Synced",
			"probe_message":   "",
		}).Error
}
