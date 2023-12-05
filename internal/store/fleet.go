package store

import (
	"fmt"
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type FleetStore struct {
	db *gorm.DB
}

// Make sure we conform to FleetStoreInterface
var _ service.FleetStoreInterface = (*FleetStore)(nil)

func NewFleetStore(db *gorm.DB) *FleetStore {
	return &FleetStore{db: db}
}

func (s *FleetStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.Fleet{})
}

func (s *FleetStore) CreateFleet(orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	fleet := model.NewFleetFromApiResource(resource)
	fleet.OrgID = orgId
	result := s.db.Create(fleet)
	log.Printf("db.Create(%s): %d rows affected, error is %v", fleet, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *FleetStore) ListFleets(orgId uuid.UUID) (*api.FleetList, error) {
	condition := model.Fleet{
		Resource: model.Resource{OrgID: orgId},
	}
	var fleets model.FleetList
	result := s.db.Where(condition).Find(&fleets)
	log.Printf("db.Where(%s).Find(): %d rows affected, error is %v", condition, result.RowsAffected, result.Error)
	apiFleetList := fleets.ToApiResource()
	return &apiFleetList, result.Error
}

func (s *FleetStore) DeleteFleets(orgId uuid.UUID) error {
	condition := model.Fleet{
		Resource: model.Resource{OrgID: orgId},
	}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	return result.Error
}

func (s *FleetStore) GetFleet(orgId uuid.UUID, name string) (*api.Fleet, error) {
	fleet := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&fleet)
	log.Printf("db.Find(%s): %d rows affected, error is %v", fleet, result.RowsAffected, result.Error)
	apiFleet := fleet.ToApiResource()
	return &apiFleet, result.Error
}

func (s *FleetStore) CreateOrUpdateFleet(orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, bool, error) {
	if resource == nil {
		return nil, false, fmt.Errorf("resource is nil")
	}
	fleet := model.NewFleetFromApiResource(resource)
	fleet.OrgID = orgId

	// don't overwrite status
	fleet.Status = nil

	var updatedFleet model.Fleet
	where := model.Fleet{Resource: model.Resource{OrgID: fleet.OrgID, Name: fleet.Name}}
	result := s.db.Where(where).Assign(fleet).FirstOrCreate(&updatedFleet)
	created := (result.RowsAffected == 0)

	updatedResource := updatedFleet.ToApiResource()
	return &updatedResource, created, result.Error
}

func (s *FleetStore) UpdateFleetStatus(orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	fleet := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: resource.Metadata.Name},
	}
	result := s.db.Model(&fleet).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return resource, result.Error
}

func (s *FleetStore) DeleteFleet(orgId uuid.UUID, name string) error {
	condition := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Unscoped().Delete(&condition)
	return result.Error
}
