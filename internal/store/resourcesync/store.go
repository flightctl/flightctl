package resourcesync

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Store interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync, callbackEvent store.EventCallback) (*domain.ResourceSync, error)
	Update(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync, callbackEvent store.EventCallback) (*domain.ResourceSync, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync, callbackEvent store.EventCallback) (*domain.ResourceSync, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ResourceSync, error)
	List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.ResourceSyncList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.RemoveOwnerCallback, callbackEvent store.EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.ResourceSync, eventCallback store.EventCallback) (*domain.ResourceSync, error)
	Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error)
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
	genericStore        *store.GenericStore[*model.ResourceSync, model.ResourceSync, domain.ResourceSync, domain.ResourceSyncList]
	eventCallbackCaller store.EventCallbackCaller
}

// Make sure we conform to the Store interface
var _ Store = (*ResourceSyncStore)(nil)

func NewResourceSyncStore(db *gorm.DB, log logrus.FieldLogger) Store {
	genericStore := store.NewGenericStore[*model.ResourceSync, model.ResourceSync, domain.ResourceSync, domain.ResourceSyncList](
		db,
		log,
		model.NewResourceSyncFromApiResource,
		(*model.ResourceSync).ToApiResource,
		model.ResourceSyncsToApiResource,
	)
	return &ResourceSyncStore{dbHandler: db, log: log, genericStore: genericStore, eventCallbackCaller: store.CallEventCallback(domain.ResourceSyncKind, log)}
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

func (s *ResourceSyncStore) Create(ctx context.Context, orgId uuid.UUID, resource *domain.ResourceSync, eventCallback store.EventCallback) (*domain.ResourceSync, error) {
	rs, err := s.genericStore.Create(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), nil, rs, true, err)
	return rs, err
}

func (s *ResourceSyncStore) Update(ctx context.Context, orgId uuid.UUID, resource *domain.ResourceSync, eventCallback store.EventCallback) (*domain.ResourceSync, error) {
	newRs, oldRs, err := s.genericStore.Update(ctx, orgId, resource, nil, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldRs, newRs, false, err)
	return newRs, err
}

func (s *ResourceSyncStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *domain.ResourceSync, eventCallback store.EventCallback) (*domain.ResourceSync, bool, error) {
	newRs, oldRs, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldRs, newRs, created, err)
	return newRs, created, err
}

func (s *ResourceSyncStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ResourceSync, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *ResourceSyncStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.ResourceSyncList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *ResourceSyncStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.RemoveOwnerCallback, callbackEvent store.EventCallback) error {
	existingRecord := model.ResourceSync{Resource: model.Resource{OrgID: orgId, Name: name}}
	err := s.getDB(ctx).Transaction(func(innerTx *gorm.DB) (err error) {
		result := innerTx.Take(&existingRecord)
		if result.Error != nil {
			return store.ErrorFromGormError(result.Error)
		}

		result = innerTx.Unscoped().Delete(&existingRecord)
		if result.Error != nil {
			return store.ErrorFromGormError(result.Error)
		}
		owner := util.SetResourceOwner(domain.ResourceSyncKind, name)
		return callback(ctx, innerTx, orgId, *owner)
	})

	if err == nil && callbackEvent != nil {
		s.eventCallbackCaller(ctx, callbackEvent, orgId, name, nil, nil, false, err)
	}
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil
		}
		return err
	}

	return nil
}

func (s *ResourceSyncStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.ResourceSync, eventCallback store.EventCallback) (*domain.ResourceSync, error) {
	// Get the old resource to compare conditions
	var oldResourceSync *domain.ResourceSync
	existingResource, err := s.Get(ctx, orgId, lo.FromPtr(resource.Metadata.Name))
	if err == nil && existingResource != nil {
		oldResourceSync = existingResource
	}

	// Update the status
	newResourceSync, err := s.genericStore.UpdateStatus(ctx, orgId, resource)
	if err != nil {
		return newResourceSync, err
	}

	// Call the event callback to emit condition-specific events
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldResourceSync, newResourceSync, false, err)

	return newResourceSync, err
}

func (s *ResourceSyncStore) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	query, err := store.ListQuery(&model.ResourceSync{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return 0, err
	}
	var resourceSyncsCount int64
	if err := query.Count(&resourceSyncsCount).Error; err != nil {
		return 0, store.ErrorFromGormError(err)
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
		return nil, store.ErrorFromGormError(err)
	}
	return results, nil
}
