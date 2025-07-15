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
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback RemoveOwnerCallback, callbackEvent EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, error)
	Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error)
	CountByOrgAndStatus(ctx context.Context, orgId *uuid.UUID, status *string) ([]CountByResourceSyncOrgAndStatusResult, error)
}

// CountByResourceSyncOrgAndStatusResult holds the result of the group by query for organization and status.
type CountByResourceSyncOrgAndStatusResult struct {
	OrgID  string
	Status string
	Count  int64
}

type ResourceSyncStore struct {
	dbHandler           *gorm.DB
	log                 logrus.FieldLogger
	genericStore        *GenericStore[*model.ResourceSync, model.ResourceSync, api.ResourceSync, api.ResourceSyncList]
	eventCallbackCaller EventCallbackCaller
}

// Make sure we conform to ResourceSync interface
var _ ResourceSync = (*ResourceSyncStore)(nil)

type RemoveOwnerCallback func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error

func NewResourceSync(db *gorm.DB, log logrus.FieldLogger) ResourceSync {
	genericStore := NewGenericStore[*model.ResourceSync, model.ResourceSync, api.ResourceSync, api.ResourceSyncList](
		db,
		log,
		model.NewResourceSyncFromApiResource,
		(*model.ResourceSync).ToApiResource,
		model.ResourceSyncsToApiResource,
	)
	return &ResourceSyncStore{dbHandler: db, log: log, genericStore: genericStore, eventCallbackCaller: CallEventCallback(api.ResourceSyncKind, log)}
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
	s.eventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), nil, rs, true, nil, err)
	return rs, err
}

func (s *ResourceSyncStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync, callbackEvent EventCallback) (*api.ResourceSync, error) {
	newRs, oldRs, updatedDetails, err := s.genericStore.Update(ctx, orgId, resource, nil, true, nil, nil)
	s.eventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), oldRs, newRs, false, &updatedDetails, err)
	return newRs, err
}

func (s *ResourceSyncStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync, callbackEvent EventCallback) (*api.ResourceSync, bool, error) {
	newRs, oldRs, created, updatedDetails, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, true, nil, nil)
	s.eventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), oldRs, newRs, created, &updatedDetails, err)
	return newRs, created, err
}

func (s *ResourceSyncStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *ResourceSyncStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ResourceSyncList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *ResourceSyncStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback RemoveOwnerCallback, callbackEvent EventCallback) error {
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

	s.eventCallbackCaller(ctx, callbackEvent, orgId, name, nil, nil, false, nil, err)
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

func (s *ResourceSyncStore) Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error) {
	query, err := ListQuery(&model.ResourceSync{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return 0, err
	}
	var resourceSyncsCount int64
	if err := query.Count(&resourceSyncsCount).Error; err != nil {
		return 0, ErrorFromGormError(err)
	}
	return resourceSyncsCount, nil
}

// CountByOrgAndStatus returns the count of resource syncs grouped by org_id and status.
func (s *ResourceSyncStore) CountByOrgAndStatus(ctx context.Context, orgId *uuid.UUID, status *string) ([]CountByResourceSyncOrgAndStatusResult, error) {
	db := s.getDB(ctx).Model(&model.ResourceSync{})
	if orgId != nil {
		db = db.Where("org_id = ?", *orgId)
	}
	if status != nil {
		db = db.Where("status = ?", *status)
	}
	var results []CountByResourceSyncOrgAndStatusResult
	err := db.Select("org_id, status, COUNT(*) as count").Group("org_id, status").Scan(&results).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	return results, nil
}
