package repository

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Store interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, error)
	Update(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Repository, error)
	List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.RepositoryList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, error)

	GetFleetRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.FleetList, error)
	GetDeviceRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.DeviceList, error)

	// Used by domain metrics
	Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error)
	CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]store.CountByOrgResult, error)
}

type RepositoryStore struct {
	dbHandler           *gorm.DB
	log                 logrus.FieldLogger
	genericStore        *store.GenericStore[*model.Repository, model.Repository, domain.Repository, domain.RepositoryList]
	eventCallbackCaller store.EventCallbackCaller
}

// Make sure we conform to the Store interface
var _ Store = (*RepositoryStore)(nil)

func NewRepositoryStore(db *gorm.DB, log logrus.FieldLogger) Store {
	genericStore := store.NewGenericStore[*model.Repository, model.Repository, domain.Repository, domain.RepositoryList](
		db,
		log,
		model.NewRepositoryFromApiResource,
		(*model.Repository).ToApiResource,
		model.RepositoriesToApiResource,
	)
	return &RepositoryStore{dbHandler: db, log: log, genericStore: genericStore, eventCallbackCaller: store.CallEventCallback(domain.RepositoryKind, log)}
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

func (s *RepositoryStore) Create(ctx context.Context, orgId uuid.UUID, resource *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, error) {
	repo, err := s.genericStore.Create(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), nil, repo, true, err)
	return repo, err
}

func (s *RepositoryStore) Update(ctx context.Context, orgId uuid.UUID, resource *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, error) {
	newRepo, oldRepo, err := s.genericStore.Update(ctx, orgId, resource, nil, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldRepo, newRepo, false, err)
	return newRepo, err
}

func (s *RepositoryStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, bool, error) {
	newRepo, oldRepo, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldRepo, newRepo, created, err)

	return newRepo, created, err
}

func (s *RepositoryStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Repository, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *RepositoryStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.RepositoryList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *RepositoryStore) ListIgnoreOrg(ctx context.Context) ([]model.Repository, error) {
	var repositories []model.Repository

	result := s.getDB(ctx).Model(&repositories).Where("spec IS NOT NULL").Find(&repositories)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return repositories, nil
}

func (s *RepositoryStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error {
	deleted, err := s.genericStore.Delete(ctx, model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}})
	if deleted && eventCallback != nil {
		s.eventCallbackCaller(ctx, eventCallback, orgId, name, nil, nil, false, nil)
	}
	return err
}

func (s *RepositoryStore) GetInternal(ctx context.Context, orgId uuid.UUID, name string) (*model.Repository, error) {
	repository := model.Repository{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.getDB(ctx).Where("spec IS NOT NULL").Take(&repository)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return &repository, nil
}

func (s *RepositoryStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, error) {
	// Get the old resource to compare conditions
	var oldRepository *domain.Repository
	existingResource, err := s.Get(ctx, orgId, lo.FromPtr(resource.Metadata.Name))
	if err == nil && existingResource != nil {
		oldRepository = existingResource
	}

	// Update the status
	newRepo, err := s.genericStore.UpdateStatus(ctx, orgId, resource)
	if err != nil {
		return newRepo, err
	}

	// Call the event callback to emit condition-specific events
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldRepository, newRepo, false, err)

	return newRepo, err
}

func (s *RepositoryStore) GetFleetRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.FleetList, error) {
	repository := model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}
	var fleets []model.Fleet
	err := s.getDB(ctx).Model(&repository).Association("Fleets").Find(&fleets)
	if err != nil {
		return nil, store.ErrorFromGormError(err)
	}
	fleetList, _ := model.FleetsToApiResource(fleets, nil, nil)
	return &fleetList, nil
}

func (s *RepositoryStore) GetDeviceRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.DeviceList, error) {
	repository := model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}
	var devices []model.Device
	err := s.getDB(ctx).Model(&repository).Association("Devices").Find(&devices)
	if err != nil {
		return nil, store.ErrorFromGormError(err)
	}
	deviceList, _ := model.DevicesToApiResource(devices, nil, nil)
	return &deviceList, nil
}

func (s *RepositoryStore) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	query, err := store.ListQuery(&model.Repository{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return 0, err
	}
	var repositoriesCount int64
	if err := query.Count(&repositoriesCount).Error; err != nil {
		return 0, store.ErrorFromGormError(err)
	}
	return repositoriesCount, nil
}

// CountByOrg returns the count of repositories grouped by org_id.
func (s *RepositoryStore) CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]store.CountByOrgResult, error) {
	var query *gorm.DB
	var err error

	if orgId != nil {
		query, err = store.ListQuery(&model.Repository{}).BuildNoOrder(ctx, s.getDB(ctx), *orgId, store.ListParams{})
	} else {
		// When orgId is nil, we don't filter by org_id
		query = s.getDB(ctx).Model(&model.Repository{})
	}

	if err != nil {
		return nil, err
	}

	query = query.Select(
		"org_id as org_id",
		"COUNT(*) as count",
	).Group("org_id")

	var results []store.CountByOrgResult
	err = query.Scan(&results).Error
	if err != nil {
		return nil, store.ErrorFromGormError(err)
	}
	return results, nil
}
