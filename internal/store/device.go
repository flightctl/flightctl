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

func (s *DeviceStore) CreateDevice(orgId uuid.UUID, resource *api.Device) (*api.Device, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	device := model.NewDeviceFromApiResource(resource)
	device.OrgID = orgId
	result := s.db.Create(device)
	log.Printf("db.Create(%s): %d rows affected, error is %v", device, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *DeviceStore) ListDevices(orgId uuid.UUID) (*api.DeviceList, error) {
	condition := model.Device{
		Resource: model.Resource{OrgID: orgId},
	}
	var devices model.DeviceList
	result := s.db.Where(condition).Find(&devices)
	log.Printf("db.Where(%s).Find(): %d rows affected, error is %v", condition, result.RowsAffected, result.Error)
	apiDevicelist := devices.ToApiResource()
	return &apiDevicelist, result.Error
}

func (s *DeviceStore) DeleteDevices(orgId uuid.UUID) error {
	condition := model.Device{
		Resource: model.Resource{OrgID: orgId},
	}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	return result.Error
}

func (s *DeviceStore) GetDevice(orgId uuid.UUID, name string) (*api.Device, error) {
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&device)
	log.Printf("db.Find(%s): %d rows affected, error is %v", device, result.RowsAffected, result.Error)
	apiDevice := device.ToApiResource()
	return &apiDevice, result.Error
}

func (s *DeviceStore) CreateOrUpdateDevice(orgId uuid.UUID, resource *api.Device) (*api.Device, bool, error) {
	if resource == nil {
		return nil, false, fmt.Errorf("resource is nil")
	}
	device := model.NewDeviceFromApiResource(resource)
	device.OrgID = orgId

	// don't overwrite status
	device.Status = nil

	var updatedDevice model.Device
	where := model.Device{Resource: model.Resource{OrgID: device.OrgID, Name: device.Name}}
	result := s.db.Where(where).Assign(device).FirstOrCreate(&updatedDevice)
	created := (result.RowsAffected == 0)

	updatedResource := updatedDevice.ToApiResource()
	return &updatedResource, created, result.Error
}

func (s *DeviceStore) UpdateDeviceStatus(orgId uuid.UUID, resource *api.Device) (*api.Device, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	if resource.Metadata.Name == nil {
		return nil, fmt.Errorf("resource.metadata.name is nil")
	}
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := s.db.Model(&device).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return resource, result.Error
}

func (s *DeviceStore) DeleteDevice(orgId uuid.UUID, name string) error {
	condition := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Unscoped().Delete(&condition)
	return result.Error
}
