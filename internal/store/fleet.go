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
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Fleet interface {
	Create(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback FleetStoreCallback) (*api.Fleet, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.FleetList, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Fleet, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback FleetStoreCallback) (*api.Fleet, bool, error)
	CreateOrUpdateMultiple(ctx context.Context, orgId uuid.UUID, callback FleetStoreCallback, fleets ...*api.Fleet) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) (*api.Fleet, error)
	UpdateStatusMultiple(ctx context.Context, orgId uuid.UUID, fleets ...*api.Fleet) error
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback FleetStoreAllDeletedCallback) error
	Delete(ctx context.Context, orgId uuid.UUID, callback FleetStoreCallback, names ...string) error
	UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error
	UnsetOwnerByKind(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, resourceKind string) error
	ListIgnoreOrg() ([]model.Fleet, error)
	UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error
	OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error
	GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error)
	InitialMigration() error
}

type FleetStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

type FleetStoreCallback func(before *model.Fleet, after *model.Fleet)
type FleetStoreAllDeletedCallback func(orgId uuid.UUID)

// Make sure we conform to Fleet interface
var _ Fleet = (*FleetStore)(nil)

func NewFleet(db *gorm.DB, log logrus.FieldLogger) Fleet {
	return &FleetStore{db: db, log: log}
}

func (s *FleetStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.Fleet{})
}

func (s *FleetStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Fleet, callback FleetStoreCallback) (*api.Fleet, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	fleet := model.NewFleetFromApiResource(resource)
	fleet.OrgID = orgId
	if fleet.Spec.Data.Template.Metadata == nil {
		fleet.Spec.Data.Template.Metadata = &api.ObjectMeta{}
	}
	fleet.Generation = util.Int64ToPtr(1)
	fleet.Annotations = nil
	fleet.Spec.Data.Template.Metadata.Generation = util.Int64ToPtr(1)
	result := s.db.Create(fleet)
	if result.Error == nil {
		callback(nil, fleet)
	}
	return resource, flterrors.ErrorFromGormError(result.Error)
}

func (s *FleetStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.FleetList, error) {
	var fleets model.FleetList
	var nextContinue *string
	var numRemaining *int64

	query := BuildBaseListQuery(s.db.Model(&fleets), orgId, listParams)
	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	}
	result := query.Find(&fleets)

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(fleets) > listParams.Limit {
		nextContinueStruct := Continue{
			Name:    fleets[len(fleets)-1].Name,
			Version: CurrentContinueVersion,
		}
		fleets = fleets[:len(fleets)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery := BuildBaseListQuery(s.db.Model(&fleets), orgId, listParams)
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiFleetList := fleets.ToApiResource(nextContinue, numRemaining)
	return &apiFleetList, flterrors.ErrorFromGormError(result.Error)
}

// A method to get all Fleets regardless of ownership. Used internally by the DeviceUpdater.
// TODO: Add pagination, perhaps via gorm scopes.
func (s *FleetStore) ListIgnoreOrg() ([]model.Fleet, error) {
	var fleets model.FleetList

	result := s.db.Model(&fleets).Find(&fleets)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	return fleets, nil
}

func (s *FleetStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback FleetStoreAllDeletedCallback) error {
	condition := model.Fleet{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	if result.Error == nil {
		callback(orgId)
	}
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *FleetStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Fleet, error) {
	fleet := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&fleet)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}

	apiFleet := fleet.ToApiResource()
	return &apiFleet, nil
}

func (s *FleetStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Fleet, callback FleetStoreCallback) (*api.Fleet, bool, error) {
	oldFleet, newFleet, err := s.createOrUpdateTx(s.db, orgId, resource)
	if err == nil {
		callback(oldFleet, newFleet)
	}
	updatedFleet := newFleet.ToApiResource()

	return &updatedFleet, oldFleet == nil, err
}

func (s *FleetStore) createOrUpdateTx(tx *gorm.DB, orgId uuid.UUID, resource *api.Fleet) (*model.Fleet, *model.Fleet, error) {
	if resource == nil {
		return nil, nil, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, nil, flterrors.ErrResourceNameIsNil
	}
	fleet := model.NewFleetFromApiResource(resource)
	fleet.OrgID = orgId

	// Use the dedicated API to update annotations
	fleet.Annotations = nil

	var existingRecord *model.Fleet

	err := tx.Transaction(func(innerTx *gorm.DB) (err error) {

		existingRecord = &model.Fleet{Resource: model.Resource{OrgID: fleet.OrgID, Name: fleet.Name}}
		result := innerTx.First(&existingRecord)
		// NotFound is OK because in that case we will create the record, anything else is a real error
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return flterrors.ErrorFromGormError(result.Error)
		}
		if result.Error != nil {
			existingRecord = nil
			if fleet.Spec.Data.Template.Metadata == nil {
				fleet.Spec.Data.Template.Metadata = &api.ObjectMeta{}
			}
			fleet.Generation = util.Int64ToPtr(1)
			fleet.Spec.Data.Template.Metadata.Generation = util.Int64ToPtr(1)

			result = innerTx.Create(fleet)
			if result.Error != nil {
				return flterrors.ErrorFromGormError(result.Error)
			}
		} else {
			// Compare owners
			// To delete / modify owner - use a different func
			resourceOwner := util.DefaultIfNil(resource.Metadata.Owner, "")
			if existingRecord.Owner != nil && *existingRecord.Owner != resourceOwner {
				return flterrors.ErrUpdatingResourceWithOwnerNotAllowed
			}
			sameSpec := reflect.DeepEqual(existingRecord.Spec.Data, fleet.Spec.Data)
			sameTemplateSpec := reflect.DeepEqual(existingRecord.Spec.Data.Template.Spec, fleet.Spec.Data.Template.Spec)

			// Update the generation if the template was updated
			if !sameSpec {
				if existingRecord.Generation == nil {
					fleet.Generation = util.Int64ToPtr(1)
				} else {
					fleet.Generation = util.Int64ToPtr(*existingRecord.Generation + 1)
				}
			} else {
				fleet.Generation = existingRecord.Generation
			}

			if fleet.Spec.Data.Template.Metadata == nil {
				fleet.Spec.Data.Template.Metadata = &api.ObjectMeta{}
			}
			if !sameTemplateSpec {
				if existingRecord.Spec.Data.Template.Metadata.Generation == nil {
					fleet.Spec.Data.Template.Metadata.Generation = util.Int64ToPtr(1)
				} else {
					fleet.Spec.Data.Template.Metadata.Generation = util.Int64ToPtr(*existingRecord.Spec.Data.Template.Metadata.Generation + 1)
				}
			} else {
				fleet.Spec.Data.Template.Metadata.Generation = existingRecord.Spec.Data.Template.Metadata.Generation
			}
			fleet.Owner = resource.Metadata.Owner

			where := model.Fleet{Resource: model.Resource{OrgID: fleet.OrgID, Name: fleet.Name}}
			query := innerTx.Model(where)

			selectFields := []string{"spec"}
			selectFields = append(selectFields, GetNonNilFieldsFromResource(fleet.Resource)...)
			query = query.Select(selectFields)
			result = query.Updates(&fleet)
			if result.Error != nil {
				return flterrors.ErrorFromGormError(result.Error)
			}
		}
		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	if existingRecord != nil {
		existingRecord.Owner = nil // Match the incoming fleet
	}
	return existingRecord, fleet, nil
}

func (s *FleetStore) CreateOrUpdateMultiple(ctx context.Context, orgId uuid.UUID, callback FleetStoreCallback, resources ...*api.Fleet) error {
	type update struct {
		oldFleet *model.Fleet
		newFleet *model.Fleet
	}
	var updates []update
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, resource := range resources {
			oldFleet, newFleet, err := s.createOrUpdateTx(tx, orgId, resource)
			if err == nil {
				updates = append(updates, update{oldFleet: oldFleet, newFleet: newFleet})
			}
			if err != nil {
				return err
			}
		}
		return nil
	})

	if err == nil {
		for i := range updates {
			callback(updates[i].oldFleet, updates[i].newFleet)
		}
	}
	return err
}

func (s *FleetStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
	return s.updateStatusTx(s.db, orgId, resource)
}

func (s *FleetStore) UpdateStatusMultiple(ctx context.Context, orgId uuid.UUID, resources ...*api.Fleet) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, resource := range resources {
			_, err := s.updateStatusTx(tx, orgId, resource)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func (s *FleetStore) updateStatusTx(tx *gorm.DB, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}
	fleet := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := tx.Model(&fleet).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return resource, flterrors.ErrorFromGormError(result.Error)
}

func (s *FleetStore) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	db := s.db
	if tx != nil {
		db = tx
	}
	fleetCondition := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Owner: &owner},
	}
	result := db.Model(fleetCondition).Where(fleetCondition).Select("owner").Updates(map[string]interface{}{"owner": nil})
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *FleetStore) UnsetOwnerByKind(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, resourceKind string) error {
	db := s.db
	if tx != nil {
		db = tx
	}
	fleetCondition := model.Fleet{
		Resource: model.Resource{OrgID: orgId},
	}
	result := db.Model(model.Fleet{}).Where(fleetCondition).Where("owner like ?", "%"+resourceKind+"/%").Select("owner").Updates(map[string]interface{}{"owner": nil})
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *FleetStore) Delete(ctx context.Context, orgId uuid.UUID, callback FleetStoreCallback, names ...string) error {
	deleted := []model.Fleet{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, name := range names {
			existingRecord := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
			result := tx.First(&existingRecord)
			if result.Error != nil {
				if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					continue
				}
				return flterrors.ErrorFromGormError(result.Error)
			}
			err := s.deleteTx(tx, orgId, name)
			if err != nil {
				return err
			}
			deleted = append(deleted, existingRecord)
		}
		return nil
	})

	if err == nil {
		for i := range deleted {
			callback(&deleted[i], nil)
		}
	}
	return err
}

func (s *FleetStore) deleteTx(tx *gorm.DB, orgId uuid.UUID, name string) error {
	condition := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := tx.Unscoped().Delete(&condition)
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *FleetStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error {
	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return flterrors.ErrorFromGormError(result.Error)
		}

		if existingRecord.Status == nil {
			existingRecord.Status = model.MakeJSONField(api.FleetStatus{})
		}
		if existingRecord.Status.Data.Conditions == nil {
			existingRecord.Status.Data.Conditions = []api.Condition{}
		}
		changed := false
		for _, condition := range conditions {
			changed = api.SetStatusCondition(&existingRecord.Status.Data.Conditions, condition)
		}
		if !changed {
			return nil
		}

		result = innerTx.Model(existingRecord).Updates(map[string]interface{}{
			"status": existingRecord.Status,
		})
		return flterrors.ErrorFromGormError(result.Error)
	})

	return err
}

func (s *FleetStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
	return s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return flterrors.ErrorFromGormError(result.Error)
		}
		existingAnnotations := util.LabelArrayToMap(existingRecord.Annotations)
		existingAnnotations = util.MergeLabels(existingAnnotations, annotations)

		for _, deleteKey := range deleteKeys {
			delete(existingAnnotations, deleteKey)
		}
		annotationsArray := util.LabelMapToArray(&existingAnnotations)

		result = innerTx.Model(existingRecord).Updates(map[string]interface{}{
			"annotations": pq.StringArray(annotationsArray),
		})
		return flterrors.ErrorFromGormError(result.Error)
	})
}

func (s *FleetStore) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	repos := []model.Repository{}
	for _, repoName := range repositoryNames {
		repos = append(repos, model.Repository{Resource: model.Resource{OrgID: orgId, Name: repoName}})
	}
	return s.db.Transaction(func(innerTx *gorm.DB) error {
		fleet := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
		err := innerTx.Model(&fleet).Association("Repositories").Clear()
		if err != nil {
			return flterrors.ErrorFromGormError(err)
		}
		if len(repos) > 0 {
			err = innerTx.Model(&fleet).Association("Repositories").Append(repos)
			return flterrors.ErrorFromGormError(err)
		}
		return nil
	})
}

func (s *FleetStore) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error) {
	fleet := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
	var repos model.RepositoryList
	err := s.db.Model(&fleet).Association("Repositories").Find(&repos)
	if err != nil {
		return nil, flterrors.ErrorFromGormError(err)
	}
	repositories, err := repos.ToApiResource(nil, nil)
	if err != nil {
		return nil, err
	}
	return &repositories, nil
}
