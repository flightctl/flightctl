package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type ResourceSync interface {
	Create(ctx context.Context, orgId uuid.UUID, repository *api.ResourceSync) (*api.ResourceSync, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ResourceSyncList, error)
	ListIgnoreOrg() ([]model.ResourceSync, error)
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback removeAllResourceSyncOwnerCallback) error
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.ResourceSync) (*api.ResourceSync, bool, error)
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
	log := log.WithReqIDFromCtx(ctx, s.log)
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	resourceSync := model.NewResourceSyncFromApiResource(resource)
	resourceSync.OrgID = orgId
	result := s.db.Create(resourceSync)
	log.Debugf("db.Create(%s): %d rows affected, error is %v", resourceSync, result.RowsAffected, result.Error)

	apiResourceSync := resourceSync.ToApiResource()
	return &apiResourceSync, result.Error
}

func (s *ResourceSyncStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ResourceSyncList, error) {
	var resourceSyncs model.ResourceSyncList
	var nextContinue *string
	var numRemaining *int64

	log := log.WithReqIDFromCtx(ctx, s.log)
	query := BuildBaseListQuery(s.db.Model(&resourceSyncs), orgId, listParams)
	// Request 1 more than the user asked for to see if we need to return "continue"
	query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	result := query.Find(&resourceSyncs)
	log.Debugf("db.Find(): %d rows affected, error is %v", result.RowsAffected, result.Error)

	// If we got more than the user requested, remove one record and calculate "continue"
	if len(resourceSyncs) > listParams.Limit {
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
	return &apiResourceSyncList, result.Error
}

func (s *ResourceSyncStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback removeAllResourceSyncOwnerCallback) error {
	condition := model.ResourceSync{
		Resource: model.Resource{OrgID: orgId},
	}
	var count int64
	result := s.db.Model(condition).Where(condition).Count(&count)
	if result.Error != nil {
		return result.Error
	}
	resourceSyncs, err := s.List(ctx, orgId, ListParams{
		Limit: int(count),
	})
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
				return result.Error
			}
			if err != nil {
				return err
			}
		}
		return callback(ctx, tx, orgId, model.ResourceSyncKind)
	})
	return err
}

func (s *ResourceSyncStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error) {
	log := log.WithReqIDFromCtx(ctx, s.log)
	resourcesync := model.ResourceSync{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&resourcesync)
	log.Debugf("db.Find(%s): %d rows affected, error is %v", resourcesync, result.RowsAffected, result.Error)
	if result.Error != nil {
		return nil, result.Error
	}
	apiResourceSync := resourcesync.ToApiResource()
	return &apiResourceSync, nil
}

func (s *ResourceSyncStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, bool, error) {
	if resource == nil {
		return nil, false, fmt.Errorf("resource is nil")
	}
	resourcesync := model.NewResourceSyncFromApiResource(resource)
	resourcesync.OrgID = orgId

	// don't overwrite status
	resourcesync.Status = nil

	created := false
	findResourceSync := model.ResourceSync{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := s.db.First(&findResourceSync)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			resourcesync.Generation = util.Int64ToPtr(1)
			created = true
		} else {
			return nil, false, result.Error
		}
	} else {
		sameSpec := reflect.DeepEqual(findResourceSync.Spec.Data, resourcesync.Spec.Data)
		resourcesync.Generation = findResourceSync.Generation
		// Update the generation if the template was updated
		if !sameSpec {
			if resourcesync.Generation == nil {
				resourcesync.Generation = util.Int64ToPtr(1)
			} else {
				resourcesync.Generation = util.Int64ToPtr(*findResourceSync.Generation + 1)
			}
		} else {
			resourcesync.Generation = findResourceSync.Generation
		}
	}

	var updatedResourceSync model.ResourceSync
	where := model.ResourceSync{Resource: model.Resource{OrgID: resourcesync.OrgID, Name: resourcesync.Name}}
	result = s.db.Where(where).Assign(resourcesync).FirstOrCreate(&updatedResourceSync)

	updatedResource := updatedResourceSync.ToApiResource()
	return &updatedResource, created, result.Error
}

func (s *ResourceSyncStore) UpdateStatusIgnoreOrg(resource *model.ResourceSync) error {
	resourcesync := model.ResourceSync{
		Resource: model.Resource{OrgID: resource.OrgID, Name: resource.Name},
	}
	result := s.db.Model(&resourcesync).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return result.Error
}

func (s *ResourceSyncStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback removeOwnerCallback) error {
	resourceSync := model.ResourceSync{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	_, err := s.Get(ctx, orgId, name)
	if err != nil {
		return err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		owner := util.SetResourceOwner(model.ResourceSyncKind, name)
		result := tx.Unscoped().Delete(&resourceSync)
		if result.Error != nil {
			return result.Error
		}
		return callback(ctx, tx, orgId, *owner)
	})
	return err
}

// A method to get all ResourceSyncs , regardless of ownership. Used internally by the the ResourceSync monitor.
// TODO: Add pagination, perhaps via gorm scopes.
func (s *ResourceSyncStore) ListIgnoreOrg() ([]model.ResourceSync, error) {
	var resourcesyncs model.ResourceSyncList
	result := s.db.Model(&resourcesyncs).Find(&resourcesyncs)
	s.log.Debugf("db.Find(): %d rows affected, error is %v", result.RowsAffected, result.Error)
	if result.Error != nil {
		return nil, result.Error
	}
	return resourcesyncs, nil
}
