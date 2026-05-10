package store

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type DependencyRef interface {
	InitialMigration(ctx context.Context) error
	Upsert(ctx context.Context, orgID uuid.UUID, ref *model.DependencyRef) error
	ListByRefType(ctx context.Context, orgID uuid.UUID, refType string) ([]model.DependencyRef, error)
	DeleteByFleet(ctx context.Context, orgID uuid.UUID, fleetName string) error
	ReplaceByFleet(ctx context.Context, orgID uuid.UUID, fleetName string, refs []model.DependencyRef) error
}

type DependencyRefStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

var _ DependencyRef = (*DependencyRefStore)(nil)

func NewDependencyRef(db *gorm.DB, log logrus.FieldLogger) DependencyRef {
	return &DependencyRefStore{dbHandler: db, log: log}
}

func (s *DependencyRefStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *DependencyRefStore) InitialMigration(ctx context.Context) error {
	return s.getDB(ctx).AutoMigrate(&model.DependencyRef{})
}

func (s *DependencyRefStore) Upsert(ctx context.Context, orgID uuid.UUID, ref *model.DependencyRef) error {
	if ref == nil {
		return fmt.Errorf("cannot upsert nil DependencyRef")
	}
	ref.OrgID = orgID
	result := s.getDB(ctx).Save(ref)
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}
	return nil
}

func (s *DependencyRefStore) ListByRefType(ctx context.Context, orgID uuid.UUID, refType string) ([]model.DependencyRef, error) {
	var refs []model.DependencyRef
	result := s.getDB(ctx).Where("org_id = ? AND ref_type = ?", orgID, refType).Find(&refs)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	return refs, nil
}

func (s *DependencyRefStore) DeleteByFleet(ctx context.Context, orgID uuid.UUID, fleetName string) error {
	result := s.getDB(ctx).Where("org_id = ? AND fleet_name = ?", orgID, fleetName).Delete(&model.DependencyRef{})
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}
	return nil
}

// ReplaceByFleet atomically replaces all dependency refs for a fleet.
// The delete and bulk insert run in a single transaction so readers never
// see a partially updated set.
func (s *DependencyRefStore) ReplaceByFleet(ctx context.Context, orgID uuid.UUID, fleetName string, refs []model.DependencyRef) error {
	return s.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("org_id = ? AND fleet_name = ?", orgID, fleetName).Delete(&model.DependencyRef{}).Error; err != nil {
			return ErrorFromGormError(err)
		}
		if len(refs) == 0 {
			return nil
		}
		for i := range refs {
			refs[i].OrgID = orgID
		}
		if err := tx.Create(&refs).Error; err != nil {
			return ErrorFromGormError(err)
		}
		return nil
	})
}
