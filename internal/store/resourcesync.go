package store

import (
	"context"
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type ResourceSync interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync, callbackEvent EventCallback) (*api.ResourceSync, error)
	Update(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync, callbackEvent EventCallback) (*api.ResourceSync, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync, callbackEvent EventCallback) (*api.ResourceSync, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ResourceSyncList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback removeOwnerCallback, callbackEvent EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, error)
}

type ResourceSyncStore struct {
	dbHandler               *gorm.DB
	log                     logrus.FieldLogger
	genericStore            *GenericStore[*model.ResourceSync, model.ResourceSync, api.ResourceSync, api.ResourceSyncList]
	callEventCallbackCaller EventCallbackCaller
}

// Make sure we conform to ResourceSync interface
var _ ResourceSync = (*ResourceSyncStore)(nil)

type removeOwnerCallback func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error

func NewResourceSync(db *gorm.DB, log logrus.FieldLogger) ResourceSync {
	genericStore := NewGenericStore[*model.ResourceSync, model.ResourceSync, api.ResourceSync, api.ResourceSyncList](
		db,
		log,
		model.NewResourceSyncFromApiResource,
		(*model.ResourceSync).ToApiResource,
		model.ResourceSyncsToApiResource,
	)
	return &ResourceSyncStore{dbHandler: db, log: log, genericStore: genericStore, callEventCallbackCaller: callEventCallback(api.ResourceSyncKind, log)}
}

func (s *ResourceSyncStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *ResourceSyncStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)

	if err := db.AutoMigrate(&model.ResourceSync{}); err != nil {
		return err
	}

	// Create GIN index for ResourceSync labels
	if !db.Migrator().HasIndex(&model.ResourceSync{}, "idx_resource_syncs_labels") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_resource_syncs_labels ON resource_syncs USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.ResourceSync{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for ResourceSync annotations
	if !db.Migrator().HasIndex(&model.ResourceSync{}, "idx_resource_syncs_annotations") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_resource_syncs_annotations ON resource_syncs USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.ResourceSync{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *ResourceSyncStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync, callbackEvent EventCallback) (*api.ResourceSync, error) {
	rs, err := s.genericStore.Create(ctx, orgId, resource, nil)
	s.callEventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), true, nil, err)
	return rs, err
}

func (s *ResourceSyncStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync, callbackEvent EventCallback) (*api.ResourceSync, error) {
	rs, _, updatedDetails, err := s.genericStore.Update(ctx, orgId, resource, nil, true, nil, nil)
	s.callEventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), false, &updatedDetails, err)
	return rs, err
}

func (s *ResourceSyncStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync, callbackEvent EventCallback) (*api.ResourceSync, bool, error) {
	rs, _, created, updatedDetails, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, true, nil, nil)
	s.callEventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), created, &updatedDetails, err)
	return rs, created, err
}

func (s *ResourceSyncStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *ResourceSyncStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ResourceSyncList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *ResourceSyncStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback removeOwnerCallback, callbackEvent EventCallback) error {
	existingRecord := model.ResourceSync{Resource: model.Resource{OrgID: orgId, Name: name}}
	err := s.getDB(ctx).Transaction(func(innerTx *gorm.DB) (err error) {
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

	s.callEventCallbackCaller(ctx, callbackEvent, orgId, name, false, nil, err)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil
		}
		return err
	}

	return nil
}

func (s *ResourceSyncStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}
