package store

import (
	"fmt"
	"log"

	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type DeviceStore struct {
	db *gorm.DB
}

// Make sure we conform to DeviceStoreInterface
var _ service.DeviceStoreInterface = (*DeviceStore)(nil)

func NewDeviceStore(db *gorm.DB) *DeviceStore {
	return &DeviceStore{db: db}
}

func (s *DeviceStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.Device{})
}

func (s *DeviceStore) CreateDevice(orgId uuid.UUID, resource *model.Device) (*model.Device, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	resource.OrgID = orgId
	result := s.db.Create(resource)
	log.Printf("db.Create: %s, %d rows affected, error is %v", resource, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *DeviceStore) ListDevices(orgId uuid.UUID) ([]model.Device, error) {
	condition := model.Device{
		Resource: model.Resource{OrgID: orgId},
	}
	var devices []model.Device
	result := s.db.Where(condition).Find(&devices)
	log.Printf("db.Find: %s, %d rows affected, error is %v", devices, result.RowsAffected, result.Error)
	return devices, result.Error
}

func (s *DeviceStore) DeleteDevices(orgId uuid.UUID) error {
	condition := model.Device{
		Resource: model.Resource{OrgID: orgId},
	}
	result := s.db.Delete(&condition)
	return result.Error
}

func (s *DeviceStore) GetDevice(orgId uuid.UUID, name string) (*model.Device, error) {
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&device)
	log.Printf("db.Find: %s, %d rows affected, error is %v", device, result.RowsAffected, result.Error)
	return &device, result.Error
}

func (s *DeviceStore) UpdateDevice(orgId uuid.UUID, resource *model.Device) (*model.Device, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: resource.Name},
	}
	result := s.db.Model(&device).Updates(map[string]interface{}{
		"spec": model.MakeJSONField(resource.Spec),
	})
	return &device, result.Error
}

func (s *DeviceStore) UpdateDeviceStatus(orgId uuid.UUID, resource *model.Device) (*model.Device, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: resource.Name},
	}
	result := s.db.Model(&device).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return &device, result.Error
}

func (s *DeviceStore) DeleteDevice(orgId uuid.UUID, name string) error {
	condition := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Delete(&condition)
	return result.Error
}
