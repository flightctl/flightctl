package store

import (
	"github.com/flightctl/flightctl/internal/model"
	"gorm.io/gorm"
)

type DeviceStore struct {
	db *gorm.DB
}

func NewDeviceStore(db *gorm.DB) *DeviceStore {
	return &DeviceStore{db: db}
}

func (d *DeviceStore) InitialMigration() error {
	return d.db.AutoMigrate(&model.Device{})
}
