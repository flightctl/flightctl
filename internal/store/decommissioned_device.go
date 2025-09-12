package store

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// DecommissionedDevice interface defines methods for managing decommissioned devices
type DecommissionedDevice interface {
	InitialMigration(ctx context.Context) error
	CreateDecommissionedDevice(ctx context.Context, orgId uuid.UUID, deviceId string, certificateExpirationDate time.Time) error
	GetDecommissionedDevice(ctx context.Context, orgId uuid.UUID, deviceId string) (*model.DecommissionedDevice, error)
	ListDecommissionedDevices(ctx context.Context, orgId uuid.UUID, listParams ListParams) ([]model.DecommissionedDevice, error)
}

// DecommissionedDeviceStore implements the DecommissionedDevice interface
type DecommissionedDeviceStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

// Make sure we conform to DecommissionedDevice interface
var _ DecommissionedDevice = (*DecommissionedDeviceStore)(nil)

// NewDecommissionedDevice creates a new DecommissionedDeviceStore instance
func NewDecommissionedDevice(db *gorm.DB, log logrus.FieldLogger) DecommissionedDevice {
	return &DecommissionedDeviceStore{dbHandler: db, log: log}
}

func (s *DecommissionedDeviceStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *DecommissionedDeviceStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)
	return db.AutoMigrate(&model.DecommissionedDevice{})
}

// CreateDecommissionedDevice creates a new entry in the decommissioned devices table
func (s *DecommissionedDeviceStore) CreateDecommissionedDevice(ctx context.Context, orgId uuid.UUID, deviceId string, certificateExpirationDate time.Time) error {
	decommissionedDevice := &model.DecommissionedDevice{
		ID:                        deviceId,
		OrgID:                     orgId,
		CertificateExpirationDate: certificateExpirationDate,
		DecommissionedAt:          time.Now(),
	}

	result := s.getDB(ctx).Create(decommissionedDevice)
	return ErrorFromGormError(result.Error)
}

// GetDecommissionedDevice retrieves a decommissioned device by ID
func (s *DecommissionedDeviceStore) GetDecommissionedDevice(ctx context.Context, orgId uuid.UUID, deviceId string) (*model.DecommissionedDevice, error) {
	var decommissionedDevice model.DecommissionedDevice
	result := s.getDB(ctx).Where("org_id = ? AND id = ?", orgId, deviceId).First(&decommissionedDevice)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	return &decommissionedDevice, nil
}

// ListDecommissionedDevices lists decommissioned devices with pagination
func (s *DecommissionedDeviceStore) ListDecommissionedDevices(ctx context.Context, orgId uuid.UUID, listParams ListParams) ([]model.DecommissionedDevice, error) {
	var decommissionedDevices []model.DecommissionedDevice

	query := s.getDB(ctx).Where("org_id = ?", orgId)

	// Apply pagination
	if listParams.Limit > 0 {
		query = query.Limit(listParams.Limit)
	}

	// Apply deterministic ordering for stable pagination
	query = query.Order("decommissioned_at DESC, id DESC")

	result := query.Find(&decommissionedDevices)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	return decommissionedDevices, nil
}
