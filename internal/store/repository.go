package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Repository interface {
	Create(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback RepositoryStoreCallback) (*api.Repository, error)
	Update(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback RepositoryStoreCallback) (*api.Repository, error)
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
	repo, _, err := s.CreateOrUpdate(ctx, orgId, resource, callback)
	return repo, err
}

func (s *RepositoryStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.Repository, callback RepositoryStoreCallback) (*api.Repository, error) {
	updatedResource, _, err := retryCreateOrUpdate(func() (*api.Repository, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, ModeUpdateOnly, callback)
	})
	return updatedResource, err
}

func (s *RepositoryStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.RepositoryList, error) {
	var repositories model.RepositoryList
	var nextContinue *string
	var numRemaining *int64

	if listParams.Limit < 0 {
		return nil, flterrors.ErrLimitInvalid
	}

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
			countQuery = countQuery.Where("spec IS NOT NULL")
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

	result := s.db.Model(&repositories).Where("spec IS NOT NULL").Find(&repositories)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	return repositories, nil
}

func (s *RepositoryStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback RepositoryStoreAllDeletedCallback) error {
	condition := model.Repository{}
	result := s.db.Unscoped().Where("spec IS NOT NULL AND org_id = ?", orgId).Delete(&condition)
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
	result := s.db.Where("spec IS NOT NULL").First(&repository)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	return &repository, nil
}

func (s *RepositoryStore) createRepository(repository *model.Repository) (bool, error) {
	repository.Generation = lo.ToPtr[int64](1)
	repository.ResourceVersion = lo.ToPtr[int64](1)
	if result := s.db.Create(repository); result.Error != nil {
		err := flterrors.ErrorFromGormError(result.Error)
		return err == flterrors.ErrDuplicateName, err
	}
	return false, nil
}

func (s *RepositoryStore) updateRepository(existingRecord, repository *model.Repository) (bool, error) {
	updateSpec := repository.Spec != nil && !reflect.DeepEqual(existingRecord.Spec, repository.Spec)

	// Update the generation if the spec was updated
	if updateSpec {
		repository.Generation = lo.ToPtr(lo.FromPtr(existingRecord.Generation) + 1)
	}
	if repository.ResourceVersion != nil && lo.FromPtr(existingRecord.ResourceVersion) != lo.FromPtr(repository.ResourceVersion) {
		return false, flterrors.ErrResourceVersionConflict
	}
	repository.ResourceVersion = lo.ToPtr(lo.FromPtr(existingRecord.ResourceVersion) + 1)
	where := model.Repository{Resource: model.Resource{OrgID: repository.OrgID, Name: repository.Name}}
	query := s.db.Model(where).Where("(resource_version is null or resource_version = ?)", lo.FromPtr(existingRecord.ResourceVersion))

	result := query.Updates(&repository)
	if result.Error != nil {
		return false, flterrors.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *RepositoryStore) createOrUpdate(orgId uuid.UUID, resource *api.Repository, mode CreateOrUpdateMode, callback RepositoryStoreCallback) (*api.Repository, bool, bool, error) {
	if resource == nil {
		return nil, false, false, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, false, false, flterrors.ErrResourceNameIsNil
	}

	repository, err := model.NewRepositoryFromApiResource(resource)
	if err != nil {
		return nil, false, false, err
	}
	repository.OrgID = orgId
	repository.Status = nil

	existingRecord, err := getExistingRecord[model.Repository](s.db, repository.Name, orgId)
	if err != nil {
		return nil, false, false, err
	}
	exists := existingRecord != nil

	if exists && mode == ModeCreateOnly {
		return nil, false, false, flterrors.ErrDuplicateName
	}
	if !exists && mode == ModeUpdateOnly {
		return nil, false, false, flterrors.ErrResourceNotFound
	}

	if !exists {
		if retry, err := s.createRepository(repository); err != nil {
			return nil, false, retry, err
		}
	} else {
		if retry, err := s.updateRepository(existingRecord, repository); err != nil {
			return nil, false, retry, err
		}
	}
	callback(repository)

	updatedResource, err := repository.ToApiResource()
	return &updatedResource, !exists || existingRecord.Spec == nil, false, err
}

func (s *RepositoryStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Repository, callback RepositoryStoreCallback) (*api.Repository, bool, error) {
	return retryCreateOrUpdate(func() (*api.Repository, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, ModeCreateOrUpdate, callback)
	})
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
	var existingRecords []*model.Repository
	if err := s.db.Raw(`delete from repositories where org_id = ? and name = ? and spec is not null returning *`, orgId, name).Scan(&existingRecords).Error; err != nil {
		return flterrors.ErrorFromGormError(err)
	}
	for i := range existingRecords {
		callback(existingRecords[i])
	}
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
	deviceList := devices.ToApiResource(nil, nil)
	return &deviceList, nil
}
