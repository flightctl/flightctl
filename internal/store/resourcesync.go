package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"reflect"

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
	Create(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ResourceSyncList, error)
	ListIgnoreOrg() ([]model.ResourceSync, error)
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback removeAllResourceSyncOwnerCallback) error
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, bool, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback removeOwnerCallback) error
	UpdateStatusIgnoreOrg(resourceSync *model.ResourceSync) error
	InitialMigration() error
}

type ResourceSyncStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// Make sure we conform to ResourceSync interface
var _ ResourceSync = (*ResourceSyncStore)(nil)

type removeOwnerCallback func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error
type removeAllResourceSyncOwnerCallback func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, kind string) error

func NewResourceSync(db *gorm.DB, log logrus.FieldLogger) ResourceSync {
	return &ResourceSyncStore{db: db, log: log}
}

func (s *ResourceSyncStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.ResourceSync{})
}

func (s *ResourceSyncStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	resourceSync, err := model.NewResourceSyncFromApiResource(resource)
	if err != nil {
		return nil, err
	}
	resourceSync.OrgID = orgId
	_, err = s.createResourceSync(resourceSync)
	if err != nil {
		return nil, err
	}
	apiResourceSync := resourceSync.ToApiResource()
	return &apiResourceSync, nil
}

func (s *ResourceSyncStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ResourceSyncList, error) {
	var resourceSyncs model.ResourceSyncList
	var nextContinue *string
	var numRemaining *int64

	query := BuildBaseListQuery(s.db.Model(&resourceSyncs), orgId, listParams)
	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	}
	result := query.Find(&resourceSyncs)

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(resourceSyncs) > listParams.Limit {
		nextContinueStruct := Continue{
			Name:    resourceSyncs[len(resourceSyncs)-1].Name,
			Version: CurrentContinueVersion,
		}
		resourceSyncs = resourceSyncs[:len(resourceSyncs)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery := BuildBaseListQuery(s.db.Model(&resourceSyncs), orgId, listParams)
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiResourceSyncList := resourceSyncs.ToApiResource(nextContinue, numRemaining)
	return &apiResourceSyncList, flterrors.ErrorFromGormError(result.Error)
}

func (s *ResourceSyncStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback removeAllResourceSyncOwnerCallback) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("org_id = ?", orgId).Delete(&model.ResourceSync{}).Error; err != nil {
			return flterrors.ErrorFromGormError(err)
		}
		return callback(ctx, tx, orgId, model.ResourceSyncKind)
	})
}

func (s *ResourceSyncStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error) {
	resourcesync := model.ResourceSync{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&resourcesync)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	apiResourceSync := resourcesync.ToApiResource()
	return &apiResourceSync, nil
}

func (s *ResourceSyncStore) createResourceSync(resourceSync *model.ResourceSync) (bool, error) {
	resourceSync.Generation = lo.ToPtr[int64](1)
	resourceSync.ResourceVersion = lo.ToPtr[int64](1)
	if result := s.db.Create(resourceSync); result.Error != nil {
		err := flterrors.ErrorFromGormError(result.Error)
		return err == flterrors.ErrDuplicateName, err
	}
	return false, nil
}

func (s *ResourceSyncStore) updateResourceSync(existingRecord, resourceSync *model.ResourceSync) (bool, error) {
	updateSpec := resourceSync.Spec != nil && !reflect.DeepEqual(existingRecord.Spec, resourceSync.Spec)

	// Update the generation if the spec was updated
	if updateSpec {
		resourceSync.Generation = lo.ToPtr(lo.FromPtr(existingRecord.Generation) + 1)
	}
	if resourceSync.ResourceVersion != nil && lo.FromPtr(existingRecord.ResourceVersion) != lo.FromPtr(resourceSync.ResourceVersion) {
		return false, flterrors.ErrResourceVersionConflict
	}
	resourceSync.ResourceVersion = lo.ToPtr(lo.FromPtr(existingRecord.ResourceVersion) + 1)
	where := model.ResourceSync{Resource: model.Resource{OrgID: resourceSync.OrgID, Name: resourceSync.Name}}
	query := s.db.Model(where).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion))

	result := query.Updates(&resourceSync)
	if result.Error != nil {
		return false, flterrors.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *ResourceSyncStore) createOrUpdate(orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, bool, bool, error) {
	if resource == nil {
		return nil, false, false, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, false, false, flterrors.ErrResourceNameIsNil
	}

	resourceSync, err := model.NewResourceSyncFromApiResource(resource)
	if err != nil {
		return nil, false, false, err
	}
	resourceSync.OrgID = orgId
	resourceSync.Status = nil

	existingRecord, err := getExistingRecord[model.ResourceSync](s.db, resourceSync.Name, orgId)
	if err != nil {
		return nil, false, false, err
	}
	exists := existingRecord != nil
	if !exists {
		if retry, err := s.createResourceSync(resourceSync); err != nil {
			return nil, false, retry, err
		}
	} else {
		if retry, err := s.updateResourceSync(existingRecord, resourceSync); err != nil {
			return nil, false, retry, err
		}
	}

	updatedResource := resourceSync.ToApiResource()
	return &updatedResource, !exists, false, err
}

func (s *ResourceSyncStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, bool, error) {
	return retryCreateOrUpdate(func() (*api.ResourceSync, bool, bool, error) {
		return s.createOrUpdate(orgId, resource)
	})
}

func (s *ResourceSyncStore) UpdateStatusIgnoreOrg(resource *model.ResourceSync) error {
	resourcesync := model.ResourceSync{
		Resource: model.Resource{OrgID: resource.OrgID, Name: resource.Name},
	}
	result := s.db.Model(&resourcesync).Updates(map[string]interface{}{
		"status":           model.MakeJSONField(resource.Status),
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *ResourceSyncStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback removeOwnerCallback) error {
	existingRecord := model.ResourceSync{Resource: model.Resource{OrgID: orgId, Name: name}}
	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return flterrors.ErrorFromGormError(result.Error)
		}

		result = innerTx.Unscoped().Delete(&existingRecord)
		if result.Error != nil {
			return flterrors.ErrorFromGormError(result.Error)
		}
		owner := util.SetResourceOwner(model.ResourceSyncKind, name)
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

// A method to get all ResourceSyncs , regardless of ownership. Used internally by the the ResourceSync monitor.
// TODO: Add pagination, perhaps via gorm scopes.
func (s *ResourceSyncStore) ListIgnoreOrg() ([]model.ResourceSync, error) {
	var resourcesyncs model.ResourceSyncList
	result := s.db.Model(&resourcesyncs).Find(&resourcesyncs)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	return resourcesyncs, nil
}
