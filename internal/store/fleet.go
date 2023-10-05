package store

import (
	"github.com/flightctl/flightctl/internal/model"
	"gorm.io/gorm"
)

type FleetStore struct {
	db *gorm.DB
}

func NewFleetStore(db *gorm.DB) *FleetStore {
	return &FleetStore{db: db}
}

func (s *FleetStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.Fleet{})
}
