package canary

import (
	"context"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store interface {
	InitialMigration(ctx context.Context) error
	Get(ctx context.Context, strategy, keyID string) (*model.EncryptionCanary, error)
	Create(ctx context.Context, canary *model.EncryptionCanary) error
	CreateOrUpdate(ctx context.Context, canary *model.EncryptionCanary) error
	Delete(ctx context.Context, strategy, keyID string) (bool, error)
	List(ctx context.Context) ([]model.EncryptionCanary, error)
}

type CanaryStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

var _ Store = (*CanaryStore)(nil)

func NewCanaryStore(db *gorm.DB, log logrus.FieldLogger) Store {
	return &CanaryStore{dbHandler: db, log: log}
}

func (s *CanaryStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *CanaryStore) InitialMigration(ctx context.Context) error {
	return s.getDB(ctx).AutoMigrate(&model.EncryptionCanary{})
}

func (s *CanaryStore) Get(ctx context.Context, strategy, keyID string) (*model.EncryptionCanary, error) {
	var row model.EncryptionCanary
	result := s.getDB(ctx).Where("strategy = ? AND key_id = ?", strategy, keyID).Take(&row)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return &row, nil
}

func (s *CanaryStore) Create(ctx context.Context, canary *model.EncryptionCanary) error {
	return store.ErrorFromGormError(s.getDB(ctx).Create(canary).Error)
}

func (s *CanaryStore) CreateOrUpdate(ctx context.Context, canary *model.EncryptionCanary) error {
	return store.ErrorFromGormError(s.getDB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "strategy"}, {Name: "key_id"}},
		UpdateAll: true,
	}).Create(canary).Error)
}

func (s *CanaryStore) Delete(ctx context.Context, strategy, keyID string) (bool, error) {
	result := s.getDB(ctx).Where("strategy = ? AND key_id = ?", strategy, keyID).Delete(&model.EncryptionCanary{})
	if result.Error != nil {
		return false, store.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	return true, nil
}

func (s *CanaryStore) List(ctx context.Context) ([]model.EncryptionCanary, error) {
	var rows []model.EncryptionCanary
	if err := s.getDB(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
