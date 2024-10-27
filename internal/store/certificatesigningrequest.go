package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type CertificateSigningRequest interface {
	Create(ctx context.Context, orgId uuid.UUID, req *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error)
	Update(ctx context.Context, orgId uuid.UUID, req *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.CertificateSigningRequestList, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.CertificateSigningRequest, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, certificatesigningrequest *api.CertificateSigningRequest) (*api.CertificateSigningRequest, bool, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, certificatesigningrequest *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error)
	DeleteAll(ctx context.Context, orgId uuid.UUID) error
	Delete(ctx context.Context, orgId uuid.UUID, name string) error
	UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error
	InitialMigration() error
}

type CertificateSigningRequestStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// Make sure we conform to CertificateSigningRequest interface
var _ CertificateSigningRequest = (*CertificateSigningRequestStore)(nil)

func NewCertificateSigningRequest(db *gorm.DB, log logrus.FieldLogger) CertificateSigningRequest {
	return &CertificateSigningRequestStore{db: db, log: log}
}

func (s *CertificateSigningRequestStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.CertificateSigningRequest{})
}

// Warning: this is a user-facing function and will set the Status to nil
func (s *CertificateSigningRequestStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error) {
	updatedResource, _, _, err := s.createOrUpdate(orgId, resource, ModeCreateOnly)
	return updatedResource, err
}

// Warning: this is a user-facing function and will set the Status to nil
func (s *CertificateSigningRequestStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error) {
	updatedResource, _, err := retryCreateOrUpdate(func() (*api.CertificateSigningRequest, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, ModeUpdateOnly)
	})
	return updatedResource, err
}

func (s *CertificateSigningRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.CertificateSigningRequestList, error) {
	var certificateSigningRequests model.CertificateSigningRequestList
	var nextContinue *string
	var numRemaining *int64

	query, err := ListQuery(&certificateSigningRequests).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return nil, err
	}

	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	}
	result := query.Find(&certificateSigningRequests)

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(certificateSigningRequests) > listParams.Limit {
		nextContinueStruct := Continue{
			Name:    certificateSigningRequests[len(certificateSigningRequests)-1].Name,
			Version: CurrentContinueVersion,
		}
		certificateSigningRequests = certificateSigningRequests[:len(certificateSigningRequests)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery, err := ListQuery(&certificateSigningRequests).Build(ctx, s.db, orgId, listParams)
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

	apiCertificateSigningRequestList := certificateSigningRequests.ToApiResource(nextContinue, numRemaining)
	return &apiCertificateSigningRequestList, ErrorFromGormError(result.Error)
}

func (s *CertificateSigningRequestStore) DeleteAll(ctx context.Context, orgId uuid.UUID) error {
	condition := model.CertificateSigningRequest{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	return ErrorFromGormError(result.Error)
}

func (s *CertificateSigningRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.CertificateSigningRequest, error) {
	certificateSigningRequest := model.CertificateSigningRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&certificateSigningRequest)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	apiCertificateSigningRequest := certificateSigningRequest.ToApiResource()
	return &apiCertificateSigningRequest, nil
}

func (s *CertificateSigningRequestStore) createCertificateSigningRequest(certificateSigningRequest *model.CertificateSigningRequest) (bool, error) {
	certificateSigningRequest.Generation = lo.ToPtr[int64](1)
	certificateSigningRequest.ResourceVersion = lo.ToPtr[int64](1)
	if result := s.db.Create(certificateSigningRequest); result.Error != nil {
		err := ErrorFromGormError(result.Error)
		return err == flterrors.ErrDuplicateName, err
	}
	return false, nil
}

func (s *CertificateSigningRequestStore) updateCertificateSigningRequest(existingRecord, certificateSigningRequest *model.CertificateSigningRequest) (bool, error) {
	updateSpec := certificateSigningRequest.Spec != nil && !reflect.DeepEqual(existingRecord.Spec, certificateSigningRequest.Spec)

	// Update the generation if the spec was updated
	if updateSpec {
		certificateSigningRequest.Generation = lo.ToPtr(lo.FromPtr(existingRecord.Generation) + 1)
	}
	if certificateSigningRequest.ResourceVersion != nil && lo.FromPtr(existingRecord.ResourceVersion) != lo.FromPtr(certificateSigningRequest.ResourceVersion) {
		return false, flterrors.ErrResourceVersionConflict
	}
	certificateSigningRequest.ResourceVersion = lo.ToPtr(lo.FromPtr(existingRecord.ResourceVersion) + 1)
	where := model.CertificateSigningRequest{Resource: model.Resource{OrgID: certificateSigningRequest.OrgID, Name: certificateSigningRequest.Name}}
	query := s.db.Model(where).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion))

	result := query.Updates(&certificateSigningRequest)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

// Warning: this is a user-facing function and will set the Status to nil
func (s *CertificateSigningRequestStore) createOrUpdate(orgId uuid.UUID, resource *api.CertificateSigningRequest, mode CreateOrUpdateMode) (*api.CertificateSigningRequest, bool, bool, error) {
	if resource == nil {
		return nil, false, false, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, false, false, flterrors.ErrResourceNameIsNil
	}

	certificatesigningrequest, err := model.NewCertificateSigningRequestFromApiResource(resource)
	if err != nil {
		return nil, false, false, err
	}
	certificatesigningrequest.OrgID = orgId
	certificatesigningrequest.Status = nil

	existingRecord, err := getExistingRecord[model.CertificateSigningRequest](s.db, certificatesigningrequest.Name, orgId)
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
		if retry, err := s.createCertificateSigningRequest(certificatesigningrequest); err != nil {
			return nil, false, retry, err
		}
	} else {
		if retry, err := s.updateCertificateSigningRequest(existingRecord, certificatesigningrequest); err != nil {
			return nil, false, retry, err
		}
	}

	updatedResource := certificatesigningrequest.ToApiResource()
	return &updatedResource, !exists, false, nil
}

func (s *CertificateSigningRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.CertificateSigningRequest) (*api.CertificateSigningRequest, bool, error) {
	return retryCreateOrUpdate(func() (*api.CertificateSigningRequest, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, ModeCreateOrUpdate)
	})
}

func (s *CertificateSigningRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}
	certificateSigningRequest := model.CertificateSigningRequest{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := s.db.Model(&certificateSigningRequest).Updates(map[string]interface{}{
		"status":           model.MakeJSONField(resource.Status),
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	return resource, ErrorFromGormError(result.Error)
}

func (s *CertificateSigningRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	condition := model.CertificateSigningRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Unscoped().Delete(&condition)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	return ErrorFromGormError(result.Error)
}

func (s *CertificateSigningRequestStore) updateConditions(orgId uuid.UUID, name string, conditions []api.Condition) (bool, error) {
	existingRecord := model.CertificateSigningRequest{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.db.First(&existingRecord)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}

	if existingRecord.Status == nil {
		existingRecord.Status = model.MakeJSONField(api.CertificateSigningRequestStatus{})
	}
	if existingRecord.Status.Data.Conditions == nil {
		existingRecord.Status.Data.Conditions = []api.Condition{}
	}
	changed := false
	for _, condition := range conditions {
		changed = api.SetStatusCondition(&existingRecord.Status.Data.Conditions, condition)
	}
	if !changed {
		return false, nil
	}

	result = s.db.Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"status":           existingRecord.Status,
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	err := ErrorFromGormError(result.Error)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *CertificateSigningRequestStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error {
	return retryUpdate(func() (bool, error) {
		return s.updateConditions(orgId, name, conditions)
	})
}
