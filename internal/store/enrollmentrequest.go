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

type EnrollmentRequest interface {
	Create(ctx context.Context, orgId uuid.UUID, req *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.EnrollmentRequestList, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, enrollmentrequest *api.EnrollmentRequest) (*api.EnrollmentRequest, bool, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, enrollmentrequest *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
	DeleteAll(ctx context.Context, orgId uuid.UUID) error
	Delete(ctx context.Context, orgId uuid.UUID, name string) error
	InitialMigration() error
}

type EnrollmentRequestStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// Make sure we conform to EnrollmentRequest interface
var _ EnrollmentRequest = (*EnrollmentRequestStore)(nil)

func NewEnrollmentRequest(db *gorm.DB, log logrus.FieldLogger) EnrollmentRequest {
	return &EnrollmentRequestStore{db: db, log: log}
}

func (s *EnrollmentRequestStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.EnrollmentRequest{})
}

func (s *EnrollmentRequestStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	enrollmentrequest := model.NewEnrollmentRequestFromApiResource(resource)
	enrollmentrequest.OrgID = orgId
	result := s.db.Create(enrollmentrequest)
	return resource, flterrors.ErrorFromGormError(result.Error)
}

func (s *EnrollmentRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.EnrollmentRequestList, error) {
	var enrollmentRequests model.EnrollmentRequestList
	var nextContinue *string
	var numRemaining *int64

	query := BuildBaseListQuery(s.db.Model(&enrollmentRequests), orgId, listParams)
	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	}
	result := query.Find(&enrollmentRequests)

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(enrollmentRequests) > listParams.Limit {
		nextContinueStruct := Continue{
			Name:    enrollmentRequests[len(enrollmentRequests)-1].Name,
			Version: CurrentContinueVersion,
		}
		enrollmentRequests = enrollmentRequests[:len(enrollmentRequests)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery := BuildBaseListQuery(s.db.Model(&enrollmentRequests), orgId, listParams)
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiEnrollmentRequestList := enrollmentRequests.ToApiResource(nextContinue, numRemaining)
	return &apiEnrollmentRequestList, flterrors.ErrorFromGormError(result.Error)
}

func (s *EnrollmentRequestStore) DeleteAll(ctx context.Context, orgId uuid.UUID) error {
	condition := model.EnrollmentRequest{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *EnrollmentRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, error) {
	enrollmentRequest := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&enrollmentRequest)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	apiEnrollmentRequest := enrollmentRequest.ToApiResource()
	return &apiEnrollmentRequest, nil
}

func (s *EnrollmentRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, bool, error) {
	if resource == nil {
		return nil, false, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, false, flterrors.ErrResourceNameIsNil
	}
	enrollmentrequest := model.NewEnrollmentRequestFromApiResource(resource)
	enrollmentrequest.OrgID = orgId

	created := false
	findEnrollmentRequest := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := s.db.First(&findEnrollmentRequest)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			created = true
		} else {
			return nil, false, flterrors.ErrorFromGormError(result.Error)
		}
	}

	enrollmentrequest.Status = nil
	var updatedEnrollmentRequest model.EnrollmentRequest
	where := model.EnrollmentRequest{Resource: model.Resource{OrgID: enrollmentrequest.OrgID, Name: enrollmentrequest.Name}}
	result = s.db.Where(where).Assign(enrollmentrequest).FirstOrCreate(&updatedEnrollmentRequest)

	updatedResource := updatedEnrollmentRequest.ToApiResource()
	return &updatedResource, created, flterrors.ErrorFromGormError(result.Error)
}

func (s *EnrollmentRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}
	enrollmentRequest := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := s.db.Model(&enrollmentRequest).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return resource, flterrors.ErrorFromGormError(result.Error)
}

func (s *EnrollmentRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	condition := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Unscoped().Delete(&condition)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	return flterrors.ErrorFromGormError(result.Error)
}
