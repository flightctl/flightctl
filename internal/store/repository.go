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

	Create(ctx context.Context, orgId uuid.UUID, repository *api.Repository, eventCallback EventCallback) (*api.Repository, error)
	Update(ctx context.Context, orgId uuid.UUID, repository *api.Repository, eventCallback EventCallback) (*api.Repository, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.Repository, eventCallback EventCallback) (*api.Repository, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.RepositoryList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Repository, eventCallback EventCallback) (*api.Repository, error)

	GetFleetRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.FleetList, error)
	GetDeviceRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceList, error)

	// Used by domain metrics
	Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error)
	CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]CountByOrgResult, error)
}

type RepositoryStore struct {
	dbHandler           *gorm.DB
	log                 logrus.FieldLogger
	genericStore        *GenericStore[*model.Repository, model.Repository, api.Repository, api.RepositoryList]
	eventCallbackCaller EventCallbackCaller
}

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
	return &RepositoryStore{dbHandler: db, log: log, genericStore: genericStore, eventCallbackCaller: CallEventCallback(api.RepositoryKind, log)}
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

func (s *RepositoryStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Repository, eventCallback EventCallback) (*api.Repository, error) {
	repo, err := s.genericStore.Create(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), nil, repo, true, err)
	return repo, err
}

func (s *RepositoryStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.Repository, eventCallback EventCallback) (*api.Repository, error) {
	newRepo, oldRepo, err := s.genericStore.Update(ctx, orgId, resource, nil, true, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldRepo, newRepo, false, err)
	return newRepo, err
}

func (s *RepositoryStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Repository, eventCallback EventCallback) (*api.Repository, bool, error) {
	newRepo, oldRepo, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, true, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldRepo, newRepo, created, err)

	return newRepo, created, err
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

func (s *RepositoryStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) error {
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
		return nil, ErrorFromGormError(result.Error)
	}
	return &repository, nil
}

func (s *RepositoryStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Repository, eventCallback EventCallback) (*api.Repository, error) {
	newRepo, err := s.genericStore.UpdateStatus(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), resource, newRepo, false, err)
	return newRepo, err
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

func (s *RepositoryStore) Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error) {
	query, err := ListQuery(&model.Repository{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return 0, err
	}
	var repositoriesCount int64
	if err := query.Count(&repositoriesCount).Error; err != nil {
		return 0, ErrorFromGormError(err)
	}
	return repositoriesCount, nil
}

// CountByOrgResult holds the result of the group by query
// for organization.
type CountByOrgResult struct {
	OrgID string
	Count int64
}

// CountByOrg returns the count of repositories grouped by org_id.
func (s *RepositoryStore) CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]CountByOrgResult, error) {
	var query *gorm.DB
	var err error

	if orgId != nil {
		query, err = ListQuery(&model.Repository{}).BuildNoOrder(ctx, s.getDB(ctx), *orgId, ListParams{})
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

	var results []CountByOrgResult
	err = query.Scan(&results).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	return results, nil
}
