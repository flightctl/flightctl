package store

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Repository interface {
	InitialMigration() error

	Create(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback RepositoryStoreCallback) (*api.Repository, error)
	Update(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback RepositoryStoreCallback) (*api.Repository, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback RepositoryStoreCallback) (*api.Repository, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.RepositoryList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback RepositoryStoreCallback) error
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback RepositoryStoreAllDeletedCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Repository) (*api.Repository, error)

	GetFleetRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.FleetList, error)
	GetDeviceRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceList, error)
}

type RepositoryStore struct {
	db           *gorm.DB
	log          logrus.FieldLogger
	genericStore *GenericStore[*model.Repository, model.Repository, api.Repository, api.RepositoryList]
}

type RepositoryStoreCallback func(uuid.UUID, *api.Repository, *api.Repository)
type RepositoryStoreAllDeletedCallback func(uuid.UUID)

// Make sure we conform to Repository interface
var _ Repository = (*RepositoryStore)(nil)

func NewRepository(db *gorm.DB, log logrus.FieldLogger) Repository {
	genericStore := NewGenericStore[*model.Repository, model.Repository, api.Repository, api.RepositoryList](
		db,
		log,
		model.NewRepositoryFromApiResource,
		(*model.Repository).ToApiResource,
		model.RepositoriesToApiResource,
	)
	return &RepositoryStore{db: db, log: log, genericStore: genericStore}
}

func (s *RepositoryStore) InitialMigration() error {
	if err := s.db.AutoMigrate(&model.Repository{}); err != nil {
		return err
	}

	// Create GIN index for Repository labels
	if !s.db.Migrator().HasIndex(&model.Repository{}, "idx_repositories_labels") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_repositories_labels ON repositories USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Repository{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for Repository annotations
	if !s.db.Migrator().HasIndex(&model.Repository{}, "idx_repositories_annotations") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_repositories_annotations ON repositories USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Repository{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *RepositoryStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Repository, callback RepositoryStoreCallback) (*api.Repository, error) {
	return s.genericStore.Create(ctx, orgId, resource, callback)
}

func (s *RepositoryStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.Repository, callback RepositoryStoreCallback) (*api.Repository, error) {
	return s.genericStore.Update(ctx, orgId, resource, nil, true, nil, callback)
}

func (s *RepositoryStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Repository, callback RepositoryStoreCallback) (*api.Repository, bool, error) {
	return s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, true, nil, callback)
}

func (s *RepositoryStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *RepositoryStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.RepositoryList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *RepositoryStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback RepositoryStoreCallback) error {
	return s.genericStore.Delete(ctx, model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}, callback)
}

func (s *RepositoryStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback RepositoryStoreAllDeletedCallback) error {
	return s.genericStore.DeleteAll(ctx, orgId, callback)
}

func (s *RepositoryStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Repository) (*api.Repository, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}

func (s *RepositoryStore) GetFleetRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.FleetList, error) {
	repository := model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}
	var fleets []model.Fleet
	err := s.db.Model(&repository).Association("Fleets").Find(&fleets)
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	fleetList, _ := model.FleetsToApiResource(fleets, nil, nil)
	return &fleetList, nil
}

func (s *RepositoryStore) GetDeviceRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceList, error) {
	repository := model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}
	var devices []model.Device
	err := s.db.Model(&repository).Association("Devices").Find(&devices)
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	deviceList, _ := model.DevicesToApiResource(devices, nil, nil)
	return &deviceList, nil
}
