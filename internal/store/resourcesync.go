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
	resourceSync := model.NewResourceSyncFromApiResource(resource)
	resourceSync.OrgID = orgId
	resourceSync.Generation = util.Int64ToPtr(1)
	result := s.db.Create(resourceSync)

	apiResourceSync := resourceSync.ToApiResource()
	return &apiResourceSync, flterrors.ErrorFromGormError(result.Error)
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
	resourceSyncs, err := s.List(ctx, orgId, ListParams{})
	if err != nil {
		return err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for _, resource := range resourceSyncs.Items {
			rsName := *resource.Metadata.Name
			resourceSync := model.ResourceSync{
				Resource: model.Resource{OrgID: orgId, Name: rsName},
			}
			result := tx.Unscoped().Delete(resourceSync)
			if result.Error != nil {
				return flterrors.ErrorFromGormError(result.Error)
			}
		}
		return callback(ctx, tx, orgId, model.ResourceSyncKind)
	})
	return err
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

func (s *ResourceSyncStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, bool, error) {
	if resource == nil {
		return nil, false, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, false, flterrors.ErrResourceNameIsNil
	}
	resourcesync := model.NewResourceSyncFromApiResource(resource)
	resourcesync.OrgID = orgId

	created := false
	var existingRecord *model.ResourceSync

	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord = &model.ResourceSync{Resource: model.Resource{OrgID: orgId, Name: resourcesync.Name}}
		result := innerTx.First(existingRecord)

		repoExists := true

		// NotFound is OK because in that case we will create the record, anything else is a real error
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				repoExists = false
			} else {
				return flterrors.ErrorFromGormError(result.Error)
			}
		}

		if !repoExists {
			created = true
			resourcesync.Generation = util.Int64ToPtr(1)

			result = innerTx.Create(resourcesync)
			if result.Error != nil {
				return flterrors.ErrorFromGormError(result.Error)
			}
		} else {
			if resource.Metadata.ResourceVersion != nil {
				if *resource.Metadata.ResourceVersion != *model.GetResourceVersion(existingRecord.UpdatedAt) {
					return flterrors.ErrResourceVersionConflict
				}
			}

			// Update the generation if the spec was updated
			sameSpec := (existingRecord.Spec == nil && resourcesync.Spec == nil) || (existingRecord.Spec != nil && resourcesync.Spec != nil && reflect.DeepEqual(existingRecord.Spec.Data, resourcesync.Spec.Data))
			if !sameSpec {
				if existingRecord.Generation == nil {
					resourcesync.Generation = util.Int64ToPtr(1)
				} else {
					resourcesync.Generation = util.Int64ToPtr(*existingRecord.Generation + 1)
				}
			} else {
				resourcesync.Generation = existingRecord.Generation
			}

			where := model.ResourceSync{Resource: model.Resource{OrgID: orgId, Name: resourcesync.Name}}
			query := innerTx.Model(where)

			selectFields := []string{"spec"}
			selectFields = append(selectFields, GetNonNilFieldsFromResource(resourcesync.Resource)...)
			query = query.Select(selectFields)
			result := query.Updates(&resourcesync)
			if result.Error != nil {
				return flterrors.ErrorFromGormError(result.Error)
			}

			result = innerTx.First(&resourcesync)
			if result.Error != nil {
				return flterrors.ErrorFromGormError(result.Error)
			}
		}

		return nil
	})

	if err != nil {
		return nil, false, err
	}

	updatedResource := resourcesync.ToApiResource()
	return &updatedResource, created, nil
}

func (s *ResourceSyncStore) UpdateStatusIgnoreOrg(resource *model.ResourceSync) error {
	resourcesync := model.ResourceSync{
		Resource: model.Resource{OrgID: resource.OrgID, Name: resource.Name},
	}
	result := s.db.Model(&resourcesync).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
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
