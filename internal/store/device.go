package store

import (
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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

func (d *DeviceStore) InitialMigration() error {
	return d.db.AutoMigrate(&model.Device{})
}

func (d *DeviceStore) CreateDevice(orgId uuid.UUID, name string) (model.Device, error) {
	device := model.Device{
		Resource: model.Resource{
			OrgID: orgId,
			Name:  name,
		},
		Spec:   model.MakeJSONField(api.DeviceSpec{}),
		Status: model.MakeJSONField(api.DeviceStatus{}),
	}
	result := d.db.Create(&device)
	log.Printf("db.Create: %s, %d rows affected, error is %v", device, result.RowsAffected, result.Error)
	return device, result.Error
}

func (d *DeviceStore) ListDevices(orgId uuid.UUID) ([]model.Device, error) {
	condition := model.Device{
		Resource: model.Resource{OrgID: orgId},
	}

	var devices []model.Device
	result := d.db.Where(condition).Find(&devices)
	log.Printf("db.Find: %s, %d rows affected, error is %v", devices, result.RowsAffected, result.Error)
	return devices, result.Error
}

func (d *DeviceStore) GetDevice(orgId uuid.UUID, name string) (model.Device, error) {
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := d.db.First(&device)
	log.Printf("db.Find: %s, %d rows affected, error is %v", device, result.RowsAffected, result.Error)
	return device, result.Error
}

func (d *DeviceStore) WriteDeviceSpec(orgId uuid.UUID, name string, spec api.DeviceSpec) error {
	return nil
}
func (d *DeviceStore) WriteDeviceStatus(orgId uuid.UUID, name string, status api.DeviceStatus) error {
	return nil
}
func (d *DeviceStore) DeleteDevices(orgId uuid.UUID) error {
	return nil
}
func (d *DeviceStore) DeleteDevice(orgId uuid.UUID, name string) error {
	return nil
}
