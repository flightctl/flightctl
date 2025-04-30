package store

import (
	"context"
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type ResourceSync interface {
	InitialMigration() error

	Create(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, error)
	Update(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, api.ResourceUpdatedDetails, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, bool, api.ResourceUpdatedDetails, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ResourceSyncList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback removeOwnerCallback) error
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback removeAllResourceSyncOwnerCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, error)
}

type ResourceSyncStore struct {
	db           *gorm.DB
	log          logrus.FieldLogger
	genericStore *GenericStore[*model.ResourceSync, model.ResourceSync, api.ResourceSync, api.ResourceSyncList]
}

// Make sure we conform to ResourceSync interface
var _ ResourceSync = (*ResourceSyncStore)(nil)

type removeOwnerCallback func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error
type removeAllResourceSyncOwnerCallback func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, kind string) error

func NewResourceSync(db *gorm.DB, log logrus.FieldLogger) ResourceSync {
	genericStore := NewGenericStore[*model.ResourceSync, model.ResourceSync, api.ResourceSync, api.ResourceSyncList](
		db,
		log,
		model.NewResourceSyncFromApiResource,
		(*model.ResourceSync).ToApiResource,
		model.ResourceSyncsToApiResource,
	)
	return &ResourceSyncStore{db: db, log: log, genericStore: genericStore}
}

func (s *ResourceSyncStore) InitialMigration() error {
	if err := s.db.AutoMigrate(&model.ResourceSync{}); err != nil {
		return err
	}

	// Create GIN index for ResourceSync labels
	if !s.db.Migrator().HasIndex(&model.ResourceSync{}, "idx_resource_syncs_labels") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_resource_syncs_labels ON resource_syncs USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.ResourceSync{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for ResourceSync annotations
	if !s.db.Migrator().HasIndex(&model.ResourceSync{}, "idx_resource_syncs_annotations") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_resource_syncs_annotations ON resource_syncs USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.ResourceSync{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *ResourceSyncStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, error) {
	return s.genericStore.Create(ctx, orgId, resource, nil)
}

func (s *ResourceSyncStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, api.ResourceUpdatedDetails, error) {
	return s.genericStore.Update(ctx, orgId, resource, nil, true, nil, nil)
}

func (s *ResourceSyncStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, bool, api.ResourceUpdatedDetails, error) {
	return s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, true, nil, nil)
}

func (s *ResourceSyncStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *ResourceSyncStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ResourceSyncList, error) {
	return s.genericStore.List(ctx, orgId, listParams, nil)
}

func (s *ResourceSyncStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback removeOwnerCallback) error {
	existingRecord := model.ResourceSync{Resource: model.Resource{OrgID: orgId, Name: name}}
	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return ErrorFromGormError(result.Error)
		}

		result = innerTx.Unscoped().Delete(&existingRecord)
		if result.Error != nil {
			return ErrorFromGormError(result.Error)
		}
		owner := util.SetResourceOwner(api.ResourceSyncKind, name)
		return callback(ctx, innerTx, orgId, *owner)
	})

	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil
		}
		return err
	}

	return nil
}

func (s *ResourceSyncStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback removeAllResourceSyncOwnerCallback) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("org_id = ?", orgId).Delete(&model.ResourceSync{}).Error; err != nil {
			return ErrorFromGormError(err)
		}
		return callback(ctx, tx, orgId, api.ResourceSyncKind)
	})
}

func (s *ResourceSyncStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}
