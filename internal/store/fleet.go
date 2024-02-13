package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
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

type Fleet interface {
	Create(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback FleetStoreCallback) (*api.Fleet, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.FleetList, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Fleet, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback FleetStoreCallback) (*api.Fleet, bool, error)
	CreateOrUpdateMultiple(ctx context.Context, orgId uuid.UUID, callback FleetStoreCallback, fleets ...*api.Fleet) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) (*api.Fleet, error)
	UpdateStatusMultiple(ctx context.Context, orgId uuid.UUID, fleets ...*api.Fleet) error
	DeleteAll(ctx context.Context, orgId uuid.UUID) error
	Delete(ctx context.Context, orgId uuid.UUID, name string) error
	ListIgnoreOrg() ([]model.Fleet, error)
	InitialMigration() error
}

type FleetStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

type FleetStoreCallback func(orgId uuid.UUID, name *string, templateUpdated bool)

// Make sure we conform to Fleet interface
var _ Fleet = (*FleetStore)(nil)

func NewFleet(db *gorm.DB, log logrus.FieldLogger) Fleet {
	return &FleetStore{db: db, log: log}
}

func (s *FleetStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.Fleet{})
}

func (s *FleetStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Fleet, callback FleetStoreCallback) (*api.Fleet, error) {
	log := log.WithReqIDFromCtx(ctx, s.log)
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	fleet := model.NewFleetFromApiResource(resource)
	fleet.OrgID = orgId
	if fleet.Spec.Data.Template.Metadata == nil {
		fleet.Spec.Data.Template.Metadata = &api.ObjectMeta{}
	}
	fleet.Generation = util.Int64ToPtr(1)
	fleet.Spec.Data.Template.Metadata.Generation = util.Int64ToPtr(1)
	callback(orgId, &fleet.Name, true)
	result := s.db.Create(fleet)
	log.Printf("db.Create(%s): %d rows affected, error is %v", fleet, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *FleetStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.FleetList, error) {
	var fleets model.FleetList
	var nextContinue *string
	var numRemaining *int64

	log := log.WithReqIDFromCtx(ctx, s.log)
	query := BuildBaseListQuery(s.db.Model(&fleets), orgId, listParams.Labels)
	// Request 1 more than the user asked for to see if we need to return "continue"
	query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	result := query.Find(&fleets)
	log.Printf("db.Find(): %d rows affected, error is %v", result.RowsAffected, result.Error)

	// If we got more than the user requested, remove one record and calculate "continue"
	if len(fleets) > listParams.Limit {
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
			countQuery := BuildBaseListQuery(s.db.Model(&fleets), orgId, listParams.Labels)
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiFleetList := fleets.ToApiResource(nextContinue, numRemaining)
	return &apiFleetList, result.Error
}

// A method to get all Fleets regardless of ownership. Used internally by the DeviceUpdater.
// TODO: Add pagination, perhaps via gorm scopes.
func (s *FleetStore) ListIgnoreOrg() ([]model.Fleet, error) {
	var fleets model.FleetList

	result := s.db.Model(&fleets).Find(&fleets)
	s.log.Debugf("db.Find(): %d rows affected, error is %v", result.RowsAffected, result.Error)
	if result.Error != nil {
		return nil, result.Error
	}
	return fleets, nil
}

func (s *FleetStore) DeleteAll(ctx context.Context, orgId uuid.UUID) error {
	condition := model.Fleet{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	return result.Error
}

func (s *FleetStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Fleet, error) {
	log := log.WithReqIDFromCtx(ctx, s.log)
	fleet := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&fleet)
	log.Printf("db.Find(%s): %d rows affected, error is %v", fleet, result.RowsAffected, result.Error)
	if result.Error != nil {
		return nil, result.Error
	}

	apiFleet := fleet.ToApiResource()
	return &apiFleet, nil
}

func (s *FleetStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Fleet, callback FleetStoreCallback) (*api.Fleet, bool, error) {
	fleet, created, templateUpdated, err := s.createOrUpdateTx(s.db, ctx, orgId, resource)
	if err == nil {
		callback(orgId, resource.Metadata.Name, templateUpdated)
	}
	return fleet, created, err
}

func (s *FleetStore) createOrUpdateTx(tx *gorm.DB, ctx context.Context, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, bool, bool, error) {
	if resource == nil {
		return nil, false, false, fmt.Errorf("resource is nil")
	}
	fleet := model.NewFleetFromApiResource(resource)
	if fleet.Name == "" {
		return nil, false, false, fmt.Errorf("resource has no name")
	}
	fleet.OrgID = orgId

	// don't allow the user to set the generation
	fleet.Generation = nil
	if fleet.Spec != nil && fleet.Spec.Data.Template.Metadata != nil {
		fleet.Spec.Data.Template.Metadata.Generation = nil
	}

	// don't overwrite status
	fleet.Status = nil

	created := false
	templateUpdated := false

	err := tx.Transaction(func(innerTx *gorm.DB) (err error) {

		existingRecord := model.Fleet{Resource: model.Resource{OrgID: fleet.OrgID, Name: fleet.Name}}
		result := innerTx.First(&existingRecord)
		// NotFound is OK because in that case we will create the record, anything else is a real error
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		if result.Error != nil {
			created = true
			if fleet.Spec.Data.Template.Metadata == nil {
				fleet.Spec.Data.Template.Metadata = &api.ObjectMeta{}
			}
			fleet.Generation = util.Int64ToPtr(1)
			fleet.Spec.Data.Template.Metadata.Generation = util.Int64ToPtr(1)
			templateUpdated = true
			result = innerTx.Create(fleet)
			if result.Error != nil {
				return result.Error
			}
		} else {
			sameSpec := reflect.DeepEqual(existingRecord.Spec.Data, fleet.Spec.Data)
			sameTemplateSpec := reflect.DeepEqual(existingRecord.Spec.Data.Template.Spec, fleet.Spec.Data.Template.Spec)
			where := model.Fleet{Resource: model.Resource{OrgID: fleet.OrgID, Name: fleet.Name}}

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

			if !sameTemplateSpec {
				templateUpdated = true
				if fleet.Spec.Data.Template.Metadata == nil {
					fleet.Spec.Data.Template.Metadata = &api.ObjectMeta{}
				}
				if existingRecord.Spec.Data.Template.Metadata.Generation == nil {
					fleet.Spec.Data.Template.Metadata.Generation = util.Int64ToPtr(1)
				} else {
					fleet.Spec.Data.Template.Metadata.Generation = util.Int64ToPtr(*existingRecord.Spec.Data.Template.Metadata.Generation + 1)
				}
			} else {
				fleet.Spec.Data.Template.Metadata.Generation = existingRecord.Spec.Data.Template.Metadata.Generation
			}
			result = innerTx.Model(where).Updates(&fleet)
			if result.Error != nil {
				return result.Error
			}
		}
		return nil
	})

	if err != nil {
		return nil, false, false, err
	}

	updatedResource := fleet.ToApiResource()
	return &updatedResource, created, templateUpdated, nil
}

func (s *FleetStore) CreateOrUpdateMultiple(ctx context.Context, orgId uuid.UUID, callback FleetStoreCallback, resources ...*api.Fleet) error {
	var updatedFleets []string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, resource := range resources {
			_, _, templateUpdated, err := s.createOrUpdateTx(tx, ctx, orgId, resource)
			if err == nil && templateUpdated {
				updatedFleets = append(updatedFleets, *resource.Metadata.Name)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})

	if err == nil {
		for i := range updatedFleets {
			callback(orgId, &updatedFleets[i], true)
		}
	}
	return err
}

func (s *FleetStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
	return s.updateStatusTx(s.db, ctx, orgId, resource)
}

func (s *FleetStore) UpdateStatusMultiple(ctx context.Context, orgId uuid.UUID, resources ...*api.Fleet) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, resource := range resources {
			_, err := s.updateStatusTx(tx, ctx, orgId, resource)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func (s *FleetStore) updateStatusTx(tx *gorm.DB, ctx context.Context, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	if resource.Metadata.Name == nil {
		return nil, fmt.Errorf("resource.metadata.name is nil")
	}
	fleet := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := s.db.Model(&fleet).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return resource, result.Error
}

func (s *FleetStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	condition := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Unscoped().Delete(&condition)
	return result.Error
}
