package store

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Repository interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback RepositoryStoreCallback, callbackEvent EventCallback) (*api.Repository, error)
	Update(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback RepositoryStoreCallback, callbackEvent EventCallback) (*api.Repository, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback RepositoryStoreCallback, callbackEvent EventCallback) (*api.Repository, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.RepositoryList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback RepositoryStoreCallback, callbackEvent EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Repository) (*api.Repository, error)

	GetFleetRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.FleetList, error)
	GetDeviceRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceList, error)
}

type RepositoryStore struct {
	dbHandler               *gorm.DB
	log                     logrus.FieldLogger
	genericStore            *GenericStore[*model.Repository, model.Repository, api.Repository, api.RepositoryList]
	callEventCallbackCaller EventCallbackCaller
}

type RepositoryStoreCallback func(context.Context, uuid.UUID, *api.Repository, *api.Repository)
type RepositoryStoreAllDeletedCallback func(context.Context, uuid.UUID)

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
	return &RepositoryStore{dbHandler: db, log: log, genericStore: genericStore, callEventCallbackCaller: callEventCallback(api.RepositoryKind, log)}
}

func (s *RepositoryStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *RepositoryStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)

	if err := db.AutoMigrate(&model.Repository{}); err != nil {
		return err
	}

	// Create GIN index for Repository labels
	if !db.Migrator().HasIndex(&model.Repository{}, "idx_repositories_labels") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_repositories_labels ON repositories USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.Repository{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for Repository annotations
	if !db.Migrator().HasIndex(&model.Repository{}, "idx_repositories_annotations") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_repositories_annotations ON repositories USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.Repository{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *RepositoryStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Repository, callback RepositoryStoreCallback, callbackEvent EventCallback) (*api.Repository, error) {
	repo, err := s.genericStore.Create(ctx, orgId, resource, callback)
	s.callEventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), true, nil, err)
	return repo, err
}

func (s *RepositoryStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.Repository, callback RepositoryStoreCallback, callbackEvent EventCallback) (*api.Repository, error) {
	repo, updatedDetails, err := s.genericStore.Update(ctx, orgId, resource, nil, true, nil, callback)
	s.callEventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), false, &updatedDetails, err)
	return repo, err
}

func (s *RepositoryStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Repository, callback RepositoryStoreCallback, callbackEvent EventCallback) (*api.Repository, bool, error) {
	repo, _, created, updatedDetails, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, true, nil, callback)
	s.callEventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), false, &updatedDetails, err)
	return repo, created, err
}

func (s *RepositoryStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *RepositoryStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.RepositoryList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

// A method to get all Repositories with secrets, regardless of ownership. Used internally by the RepoTester.
// TODO: Add pagination, perhaps via gorm scopes.
func (s *RepositoryStore) ListIgnoreOrg(ctx context.Context) ([]model.Repository, error) {
	var repositories []model.Repository

	result := s.getDB(ctx).Model(&repositories).Where("spec IS NOT NULL").Find(&repositories)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	return repositories, nil
}

func (s *RepositoryStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback RepositoryStoreCallback, callbackEvent EventCallback) error {
	_, err := s.genericStore.Delete(ctx, model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}, callback)
	s.callEventCallbackCaller(ctx, callbackEvent, orgId, name, false, nil, err)
	return err
}

func (s *RepositoryStore) GetInternal(ctx context.Context, orgId uuid.UUID, name string) (*model.Repository, error) {
	repository := model.Repository{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.getDB(ctx).Where("spec IS NOT NULL").First(&repository)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	return &repository, nil
}

func (s *RepositoryStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Repository) (*api.Repository, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}

func (s *RepositoryStore) GetFleetRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.FleetList, error) {
	repository := model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}
	var fleets []model.Fleet
	err := s.getDB(ctx).Model(&repository).Association("Fleets").Find(&fleets)
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	fleetList, _ := model.FleetsToApiResource(fleets, nil, nil)
	return &fleetList, nil
}

func (s *RepositoryStore) GetDeviceRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceList, error) {
	repository := model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}
	var devices []model.Device
	err := s.getDB(ctx).Model(&repository).Association("Devices").Find(&devices)
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	deviceList, _ := model.DevicesToApiResource(devices, nil, nil)
	return &deviceList, nil
}
