package store

import (
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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

func (s *FleetStore) CreateFleet(orgId uuid.UUID, name string) (model.Fleet, error) {
	resource := model.Fleet{
		Resource: model.Resource{
			OrgID: orgId,
			Name:  name,
		},
		Spec:   model.MakeJSONField(api.FleetSpec{}),
		Status: model.MakeJSONField(api.FleetStatus{}),
	}
	result := s.db.Create(&resource)
	log.Printf("db.Create: %s, %d rows affected, error is %v", resource, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *FleetStore) ListFleets(orgId uuid.UUID) ([]model.Fleet, error) {
	condition := model.Fleet{
		Resource: model.Resource{OrgID: orgId},
	}

	var devices []model.Fleet
	result := s.db.Where(condition).Find(&devices)
	log.Printf("db.Find: %s, %d rows affected, error is %v", devices, result.RowsAffected, result.Error)
	return devices, result.Error
}

func (s *FleetStore) GetFleet(orgId uuid.UUID, name string) (model.Fleet, error) {
	device := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&device)
	log.Printf("db.Find: %s, %d rows affected, error is %v", device, result.RowsAffected, result.Error)
	return device, result.Error
}

func (s *FleetStore) WriteFleetSpec(orgId uuid.UUID, name string, spec api.FleetSpec) error {
	return nil
}
func (s *FleetStore) WriteFleetStatus(orgId uuid.UUID, name string, status api.FleetStatus) error {
	return nil
}
func (s *FleetStore) DeleteFleets(orgId uuid.UUID) error {
	return nil
}
func (s *FleetStore) DeleteFleet(orgId uuid.UUID, name string) error {
	return nil
}
