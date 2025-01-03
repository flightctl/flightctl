package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type EnrollmentRequest interface {
	Create(ctx context.Context, orgId uuid.UUID, req *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
	Update(ctx context.Context, orgId uuid.UUID, req *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
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
	updatedResource, _, _, err := s.createOrUpdate(orgId, resource, ModeCreateOnly)
	return updatedResource, err
}

func (s *EnrollmentRequestStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, error) {
	updatedResource, _, err := retryCreateOrUpdate(func() (*api.EnrollmentRequest, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, ModeUpdateOnly)
	})
	return updatedResource, err
}

func (s *EnrollmentRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.EnrollmentRequestList, error) {
	var enrollmentRequests model.EnrollmentRequestList
	var nextContinue *string
	var numRemaining *int64

	if listParams.Limit < 0 {
		return nil, flterrors.ErrLimitParamOutOfBounds
	}

	query, err := ListQuery(&model.EnrollmentRequest{}).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return nil, err
	}

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
			countQuery, err := ListQuery(&model.EnrollmentRequest{}).Build(ctx, s.db, orgId, listParams)
			if err != nil {
				return nil, err
			}
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiEnrollmentRequestList := enrollmentRequests.ToApiResource(nextContinue, numRemaining)
	return &apiEnrollmentRequestList, ErrorFromGormError(result.Error)
}

func (s *EnrollmentRequestStore) DeleteAll(ctx context.Context, orgId uuid.UUID) error {
	condition := model.EnrollmentRequest{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	return ErrorFromGormError(result.Error)
}

func (s *EnrollmentRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, error) {
	enrollmentRequest := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&enrollmentRequest)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	apiEnrollmentRequest := enrollmentRequest.ToApiResource()
	return &apiEnrollmentRequest, nil
}

func (s *EnrollmentRequestStore) createEnrollmentRequest(enrollmentRequest *model.EnrollmentRequest) (bool, error) {
	enrollmentRequest.Generation = lo.ToPtr[int64](1)
	enrollmentRequest.ResourceVersion = lo.ToPtr[int64](1)
	if result := s.db.Create(enrollmentRequest); result.Error != nil {
		err := ErrorFromGormError(result.Error)
		return err == flterrors.ErrDuplicateName, err
	}
	return false, nil
}

func (s *EnrollmentRequestStore) updateEnrollmentRequest(existingRecord, enrollmentRequest *model.EnrollmentRequest) (bool, error) {
	updateSpec := enrollmentRequest.Spec != nil && !reflect.DeepEqual(existingRecord.Spec, enrollmentRequest.Spec)

	// Update the generation if the spec was updated
	if updateSpec {
		enrollmentRequest.Generation = lo.ToPtr(lo.FromPtr(existingRecord.Generation) + 1)
	}
	if enrollmentRequest.ResourceVersion != nil && lo.FromPtr(existingRecord.ResourceVersion) != lo.FromPtr(enrollmentRequest.ResourceVersion) {
		return false, flterrors.ErrResourceVersionConflict
	}
	enrollmentRequest.ResourceVersion = lo.ToPtr(lo.FromPtr(existingRecord.ResourceVersion) + 1)
	where := model.EnrollmentRequest{Resource: model.Resource{OrgID: enrollmentRequest.OrgID, Name: enrollmentRequest.Name}}
	query := s.db.Model(where).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion))

	result := query.Updates(&enrollmentRequest)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *EnrollmentRequestStore) createOrUpdate(orgId uuid.UUID, resource *api.EnrollmentRequest, mode CreateOrUpdateMode) (*api.EnrollmentRequest, bool, bool, error) {
	if resource == nil {
		return nil, false, false, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, false, false, flterrors.ErrResourceNameIsNil
	}

	enrollmentrequest, err := model.NewEnrollmentRequestFromApiResource(resource)
	if err != nil {
		return nil, false, false, err
	}
	enrollmentrequest.OrgID = orgId
	enrollmentrequest.Status = nil

	existingRecord, err := getExistingRecord[model.EnrollmentRequest](s.db, enrollmentrequest.Name, orgId)
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
		if retry, err := s.createEnrollmentRequest(enrollmentrequest); err != nil {
			return nil, false, retry, err
		}
	} else {
		if retry, err := s.updateEnrollmentRequest(existingRecord, enrollmentrequest); err != nil {
			return nil, false, retry, err
		}
	}

	updatedResource := enrollmentrequest.ToApiResource()
	return &updatedResource, !exists, false, nil
}

func (s *EnrollmentRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, bool, error) {
	return retryCreateOrUpdate(func() (*api.EnrollmentRequest, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, ModeCreateOrUpdate)
	})
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
		"status":           model.MakeJSONField(resource.Status),
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	return resource, ErrorFromGormError(result.Error)
}

func (s *EnrollmentRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	condition := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Unscoped().Delete(&condition)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	return ErrorFromGormError(result.Error)
}
