package store

import (
	"github.com/flightctl/flightctl/internal/service"
	"gorm.io/gorm"
)

type Store struct {
	deviceStore            *DeviceStore
	enrollmentRequestStore *EnrollmentRequestStore
	fleetStore             *FleetStore
}

func NewStore(db *gorm.DB) *Store {
	return &Store{
		deviceStore:            NewDeviceStore(db),
		enrollmentRequestStore: NewEnrollmentRequestStoreStore(db),
		fleetStore:             NewFleetStore(db),
	}
}

func (s *Store) GetDeviceStore() service.DeviceStoreInterface {
	return s.deviceStore
}

func (s *Store) GetEnrollmentRequestStore() service.EnrollmentRequestStoreInterface {
	return s.enrollmentRequestStore
}

func (s *Store) GetFleetStore() service.FleetStoreInterface {
	return s.fleetStore
}

func (s *Store) InitialMigration() error {
	if err := s.deviceStore.InitialMigration(); err != nil {
		return err
	}
	if err := s.enrollmentRequestStore.InitialMigration(); err != nil {
		return err
	}
	if err := s.fleetStore.InitialMigration(); err != nil {
		return err
	}
	return nil
}
