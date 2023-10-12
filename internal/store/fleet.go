package store

import (
	"fmt"
	"log"

	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/internal/service"
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

func (s *FleetStore) CreateFleet(orgId uuid.UUID, resource *model.Fleet) (*model.Fleet, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	resource.OrgID = orgId
	result := s.db.Create(resource)
	log.Printf("db.Create: %s, %d rows affected, error is %v", resource, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *FleetStore) ListFleets(orgId uuid.UUID) ([]model.Fleet, error) {
	condition := model.Fleet{
		Resource: model.Resource{OrgID: orgId},
	}
	var fleets []model.Fleet
	result := s.db.Where(condition).Find(&fleets)
	log.Printf("db.Find: %s, %d rows affected, error is %v", fleets, result.RowsAffected, result.Error)
	return fleets, result.Error
}

func (s *FleetStore) DeleteFleets(orgId uuid.UUID) error {
	condition := model.Fleet{
		Resource: model.Resource{OrgID: orgId},
	}
	result := s.db.Delete(&condition)
	return result.Error
}

func (s *FleetStore) GetFleet(orgId uuid.UUID, name string) (*model.Fleet, error) {
	fleet := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&fleet)
	log.Printf("db.Find: %s, %d rows affected, error is %v", fleet, result.RowsAffected, result.Error)
	return &fleet, result.Error
}

func (s *FleetStore) CreateOrUpdateFleet(orgId uuid.UUID, resource *model.Fleet) (*model.Fleet, bool, error) {
	if resource == nil {
		return nil, false, fmt.Errorf("resource is nil")
	}
	if resource.Spec != nil {
		resource.Spec = nil
	}
	resource.OrgID = orgId
	where := model.Fleet{Resource: model.Resource{OrgID: resource.OrgID, Name: resource.Name}}
	result := s.db.Where(where).Assign(resource).FirstOrCreate(resource)
	created := (result.RowsAffected == 0)
	return resource, created, result.Error
}

func (s *FleetStore) UpdateFleetStatus(orgId uuid.UUID, resource *model.Fleet) (*model.Fleet, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	fleet := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: resource.Name},
	}
	result := s.db.Model(&fleet).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return &fleet, result.Error
}

func (s *FleetStore) DeleteFleet(orgId uuid.UUID, name string) error {
	condition := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Delete(&condition)
	return result.Error
}
