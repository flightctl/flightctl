package store

import (
	"github.com/flightctl/flightctl/internal/service"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Store struct {
	deviceStore            *DeviceStore
	enrollmentRequestStore *EnrollmentRequestStore
	fleetStore             *FleetStore
	repositoryStore        *RepositoryStore
	resourceSyncStore      *ResourceSyncStore
}

func NewStore(db *gorm.DB, log logrus.FieldLogger) *Store {
	return &Store{
		deviceStore:            NewDeviceStore(db, log),
		enrollmentRequestStore: NewEnrollmentRequestStoreStore(db, log),
		fleetStore:             NewFleetStore(db, log),
		repositoryStore:        NewRepositoryStore(db, log),
		resourceSyncStore:      NewResourceSyncStore(db, log),
	}
}

func (s *Store) GetRepositoryStore() service.RepositoryStoreInterface {
	return s.repositoryStore
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

func (s *Store) GetResourceSyncStore() service.ResourceSyncStoreInterface {
	return s.resourceSyncStore
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
	if err := s.repositoryStore.InitialMigration(); err != nil {
		return err
	}
	if err := s.resourceSyncStore.InitialMigration(); err != nil {
		return err
	}
	return nil
}
