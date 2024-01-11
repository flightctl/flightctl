package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type RepositoryStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// Make sure we conform to RepositoryStoreInterface
var _ service.RepositoryStoreInterface = (*RepositoryStore)(nil)

func NewRepositoryStore(db *gorm.DB, log logrus.FieldLogger) *RepositoryStore {
	return &RepositoryStore{db: db, log: log}
}

func (s *RepositoryStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.Repository{})
}

func (s *RepositoryStore) CreateRepository(ctx context.Context, orgId uuid.UUID, resource *api.Repository) (*api.RepositoryRead, error) {
	log := log.WithReqID(ctx, s.log)
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	repository := model.NewRepositoryFromApiResource(resource)
	repository.OrgID = orgId
	result := s.db.Create(repository)
	log.Printf("db.Create(%s): %d rows affected, error is %v", repository, result.RowsAffected, result.Error)

	apiRepository := repository.ToApiResource()
	return &apiRepository, result.Error
}

func (s *RepositoryStore) ListRepositories(ctx context.Context, orgId uuid.UUID, listParams service.ListParams) (*api.RepositoryList, error) {
	var repositories model.RepositoryList
	var nextContinue *string
	var numRemaining *int64

	log := log.WithReqID(ctx, s.log)
	query := BuildBaseListQuery(s.db.Model(&repositories), orgId, listParams.Labels)
	// Request 1 more than the user asked for to see if we need to return "continue"
	query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	result := query.Find(&repositories)
	log.Printf("db.Find(): %d rows affected, error is %v", result.RowsAffected, result.Error)

	// If we got more than the user requested, remove one record and calculate "continue"
	if len(repositories) > listParams.Limit {
		nextContinueStruct := service.Continue{
			Name:    repositories[len(repositories)-1].Name,
			Version: service.CurrentContinueVersion,
		}
		repositories = repositories[:len(repositories)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery := BuildBaseListQuery(s.db.Model(&repositories), orgId, listParams.Labels)
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiRepositoryList := repositories.ToApiResource(nextContinue, numRemaining)
	return &apiRepositoryList, result.Error
}

func (s *RepositoryStore) DeleteRepositories(ctx context.Context, orgId uuid.UUID) error {
	condition := model.Repository{
		Resource: model.Resource{OrgID: orgId},
	}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	return result.Error
}

func (s *RepositoryStore) GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryRead, error) {
	log := log.WithReqID(ctx, s.log)
	repository := model.Repository{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&repository)
	log.Printf("db.Find(%s): %d rows affected, error is %v", repository, result.RowsAffected, result.Error)
	apiRepository := repository.ToApiResource()
	return &apiRepository, result.Error
}

func (s *RepositoryStore) CreateOrUpdateRepository(ctx context.Context, orgId uuid.UUID, resource *api.Repository) (*api.RepositoryRead, bool, error) {
	if resource == nil {
		return nil, false, fmt.Errorf("resource is nil")
	}
	Repository := model.NewRepositoryFromApiResource(resource)
	Repository.OrgID = orgId

	// don't overwrite status
	Repository.Status = nil

	var updatedRepository model.Repository
	where := model.Repository{Resource: model.Resource{OrgID: Repository.OrgID, Name: Repository.Name}}
	result := s.db.Where(where).Assign(Repository).FirstOrCreate(&updatedRepository)
	created := (result.RowsAffected == 0)

	updatedResource := updatedRepository.ToApiResource()
	return &updatedResource, created, result.Error
}

func (s *RepositoryStore) DeleteRepository(ctx context.Context, orgId uuid.UUID, name string) error {
	condition := model.Repository{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Unscoped().Delete(&condition)
	return result.Error
}
