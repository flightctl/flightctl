package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Repository interface {
	Create(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback RepositoryStoreCallback) (*api.Repository, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.RepositoryList, error)
	ListIgnoreOrg() ([]model.Repository, error)
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback RepositoryStoreAllDeletedCallback) error
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error)
	GetInternal(ctx context.Context, orgId uuid.UUID, name string) (*model.Repository, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback RepositoryStoreCallback) (*api.Repository, bool, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback RepositoryStoreCallback) error
	UpdateStatusIgnoreOrg(repository *model.Repository) error
	GetFleetRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.FleetList, error)
	GetDeviceRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceList, error)
	InitialMigration() error
}

type RepositoryStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

type RepositoryStoreCallback func(*model.Repository)
type RepositoryStoreAllDeletedCallback func(uuid.UUID)

// Make sure we conform to Repository interface
var _ Repository = (*RepositoryStore)(nil)

func NewRepository(db *gorm.DB, log logrus.FieldLogger) Repository {
	return &RepositoryStore{db: db, log: log}
}

func (s *RepositoryStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.Repository{})
}

func (s *RepositoryStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Repository, callback RepositoryStoreCallback) (*api.Repository, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	repository := model.NewRepositoryFromApiResource(resource)
	repository.OrgID = orgId
	result := s.db.Create(repository)
	apiRepository, toApiErr := repository.ToApiResource()
	if result.Error == nil {
		callback(repository)
	}
	err := flterrors.ErrorFromGormError(result.Error)
	if err == nil {
		err = toApiErr
	}
	return &apiRepository, err
}

func (s *RepositoryStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.RepositoryList, error) {
	var repositories model.RepositoryList
	var nextContinue *string
	var numRemaining *int64

	query := BuildBaseListQuery(s.db.Model(&repositories), orgId, listParams)
	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	}
	query = query.Where("spec IS NOT NULL")
	result := query.Find(&repositories)

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(repositories) > listParams.Limit {
		nextContinueStruct := Continue{
			Name:    repositories[len(repositories)-1].Name,
			Version: CurrentContinueVersion,
		}
		repositories = repositories[:len(repositories)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery := BuildBaseListQuery(s.db.Model(&repositories), orgId, listParams)
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiRepositoryList, toApiErr := repositories.ToApiResource(nextContinue, numRemaining)
	err := flterrors.ErrorFromGormError(result.Error)
	if err == nil {
		err = toApiErr
	}
	return &apiRepositoryList, err
}

// A method to get all Repositories with secrets, regardless of ownership. Used internally by the RepoTester.
// TODO: Add pagination, perhaps via gorm scopes.
func (s *RepositoryStore) ListIgnoreOrg() ([]model.Repository, error) {
	var repositories model.RepositoryList

	result := s.db.Model(&repositories).Find(&repositories)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	return repositories, nil
}

func (s *RepositoryStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback RepositoryStoreAllDeletedCallback) error {
	condition := model.Repository{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	if result.Error == nil {
		callback(orgId)
	}
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *RepositoryStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error) {
	repository, err := s.GetInternal(ctx, orgId, name)
	if err != nil {
		return nil, err
	}
	apiRepository, err := repository.ToApiResource()
	return &apiRepository, err
}

func (s *RepositoryStore) GetInternal(ctx context.Context, orgId uuid.UUID, name string) (*model.Repository, error) {
	repository := model.Repository{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&repository)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	return &repository, nil
}

func (s *RepositoryStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Repository, callback RepositoryStoreCallback) (*api.Repository, bool, error) {
	if resource == nil {
		return nil, false, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, false, flterrors.ErrResourceNameIsNil
	}
	repository := model.NewRepositoryFromApiResource(resource)
	repository.OrgID = orgId

	created := false
	findRepository := model.Repository{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := s.db.First(&findRepository)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			created = true
		} else {
			return nil, false, flterrors.ErrorFromGormError(result.Error)
		}
	}

	var updatedRepository model.Repository
	where := model.Repository{Resource: model.Resource{OrgID: repository.OrgID, Name: repository.Name}}
	result = s.db.Where(where).Assign(repository).FirstOrCreate(&updatedRepository)

	updatedResource, toApiErr := updatedRepository.ToApiResource()
	if result.Error == nil {
		callback(repository)
	}
	err := flterrors.ErrorFromGormError(result.Error)
	if err == nil {
		err = toApiErr
	}
	return &updatedResource, created, err
}

func (s *RepositoryStore) UpdateStatusIgnoreOrg(resource *model.Repository) error {
	repository := model.Repository{
		Resource: model.Resource{OrgID: resource.OrgID, Name: resource.Name},
	}
	result := s.db.Model(&repository).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *RepositoryStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback RepositoryStoreCallback) error {
	var existingRecord model.Repository
	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord = model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return flterrors.ErrorFromGormError(result.Error)
		}

		if err := innerTx.Unscoped().Delete(&existingRecord).Error; err != nil {
			return flterrors.ErrorFromGormError(err)
		}
		return nil
	})

	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil
		}
		return err
	}

	callback(&existingRecord)
	return nil
}

func (s *RepositoryStore) GetFleetRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.FleetList, error) {
	repository := model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}
	var fleets model.FleetList
	err := s.db.Model(&repository).Association("Fleets").Find(&fleets)
	if err != nil {
		return nil, flterrors.ErrorFromGormError(err)
	}
	fleetList := fleets.ToApiResource(nil, nil)
	return &fleetList, nil
}

func (s *RepositoryStore) GetDeviceRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceList, error) {
	repository := model.Repository{Resource: model.Resource{OrgID: orgId, Name: name}}
	var devices model.DeviceList
	err := s.db.Model(&repository).Association("Devices").Find(&devices)
	if err != nil {
		return nil, flterrors.ErrorFromGormError(err)
	}
	deviceList := devices.ToApiResource(nil, nil, nil)
	return &deviceList, nil
}
