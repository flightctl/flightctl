package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type FleetStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// Make sure we conform to FleetStoreInterface
var _ service.FleetStoreInterface = (*FleetStore)(nil)

func NewFleetStore(db *gorm.DB, log logrus.FieldLogger) *FleetStore {
	return &FleetStore{db: db, log: log}
}

func (s *FleetStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.Fleet{})
}

func (s *FleetStore) CreateFleet(ctx context.Context, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
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
	result := s.db.Create(fleet)
	log.Printf("db.Create(%s): %d rows affected, error is %v", fleet, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *FleetStore) ListFleets(ctx context.Context, orgId uuid.UUID, listParams service.ListParams) (*api.FleetList, error) {
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
		nextContinueStruct := service.Continue{
			Name:    fleets[len(fleets)-1].Name,
			Version: service.CurrentContinueVersion,
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

func (s *FleetStore) DeleteFleets(ctx context.Context, orgId uuid.UUID) error {
	condition := model.Fleet{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	return result.Error
}

func (s *FleetStore) GetFleet(ctx context.Context, orgId uuid.UUID, name string) (*api.Fleet, error) {
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

func (s *FleetStore) CreateOrUpdateFleet(ctx context.Context, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, bool, error) {
	if resource == nil {
		return nil, false, fmt.Errorf("resource is nil")
	}
	fleet := model.NewFleetFromApiResource(resource)
	fleet.OrgID = orgId

	// don't allow the user to set the generation
	fleet.Generation = nil
	if fleet.Spec != nil && fleet.Spec.Data.Template.Metadata != nil {
		fleet.Spec.Data.Template.Metadata.Generation = nil
	}

	// don't overwrite status
	fleet.Status = nil

	created := false

	err := s.db.Transaction(func(tx *gorm.DB) error {
		existingRecord := model.Fleet{Resource: model.Resource{OrgID: fleet.OrgID, Name: fleet.Name}}
		result := tx.First(&existingRecord)
		if result.Error != nil {
			if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
				return result.Error
			}

			created = true
			if fleet.Spec.Data.Template.Metadata == nil {
				fleet.Spec.Data.Template.Metadata = &api.ObjectMeta{}
			}
			fleet.Generation = util.Int64ToPtr(1)
			fleet.Spec.Data.Template.Metadata.Generation = util.Int64ToPtr(1)
			result = tx.Create(fleet)
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
			result = tx.Model(where).Updates(&fleet)
			if result.Error != nil {
				return result.Error
			}
		}
		return nil
	})

	if err != nil {
		return nil, false, err
	}

	updatedResource := fleet.ToApiResource()
	return &updatedResource, created, nil
}

func (s *FleetStore) UpdateFleetStatus(ctx context.Context, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
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

func (s *FleetStore) DeleteFleet(ctx context.Context, orgId uuid.UUID, name string) error {
	condition := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Unscoped().Delete(&condition)
	return result.Error
}
