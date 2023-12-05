package store

import (
	"fmt"
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type EnrollmentRequestStore struct {
	db *gorm.DB
}

// Make sure we conform to EnrollmentRequestStoreInterface
var _ service.EnrollmentRequestStoreInterface = (*EnrollmentRequestStore)(nil)

func NewEnrollmentRequestStoreStore(db *gorm.DB) *EnrollmentRequestStore {
	return &EnrollmentRequestStore{db: db}
}

func (s *EnrollmentRequestStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.EnrollmentRequest{})
}

func (s *EnrollmentRequestStore) CreateEnrollmentRequest(orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	enrollmentrequest := model.NewEnrollmentRequestFromApiResource(resource)
	enrollmentrequest.OrgID = orgId
	result := s.db.Create(enrollmentrequest)
	log.Printf("db.Create(%s): %d rows affected, error is %v", enrollmentrequest, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *EnrollmentRequestStore) ListEnrollmentRequests(orgId uuid.UUID) (*api.EnrollmentRequestList, error) {
	condition := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId},
	}
	var enrollmentRequests model.EnrollmentRequestList
	result := s.db.Where(condition).Find(&enrollmentRequests)
	log.Printf("db.Where(%s).Find(): %d rows affected, error is %v", condition, result.RowsAffected, result.Error)
	apiEnrollmentRequestList := enrollmentRequests.ToApiResource()
	return &apiEnrollmentRequestList, result.Error
}

func (s *EnrollmentRequestStore) DeleteEnrollmentRequests(orgId uuid.UUID) error {
	condition := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId},
	}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	return result.Error
}

func (s *EnrollmentRequestStore) GetEnrollmentRequest(orgId uuid.UUID, name string) (*api.EnrollmentRequest, error) {
	enrollmentRequest := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&enrollmentRequest)
	log.Printf("db.Find(%s): %d rows affected, error is %v", name, result.RowsAffected, result.Error)
	apiEnrollmentRequest := enrollmentRequest.ToApiResource()
	return &apiEnrollmentRequest, result.Error
}

func (s *EnrollmentRequestStore) CreateOrUpdateEnrollmentRequest(orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, bool, error) {
	if resource == nil {
		return nil, false, fmt.Errorf("resource is nil")
	}
	enrollmentrequest := model.NewEnrollmentRequestFromApiResource(resource)
	enrollmentrequest.OrgID = orgId

	// don't overwrite status
	enrollmentrequest.Status = nil

	var updatedEnrollmentRequest model.EnrollmentRequest
	where := model.EnrollmentRequest{Resource: model.Resource{OrgID: enrollmentrequest.OrgID, Name: enrollmentrequest.Name}}
	result := s.db.Where(where).Assign(enrollmentrequest).FirstOrCreate(&updatedEnrollmentRequest)
	created := (result.RowsAffected == 0)

	updatedResource := updatedEnrollmentRequest.ToApiResource()
	return &updatedResource, created, result.Error
}

func (s *EnrollmentRequestStore) UpdateEnrollmentRequestStatus(orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	enrollmentRequest := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: resource.Metadata.Name},
	}
	result := s.db.Model(&enrollmentRequest).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return resource, result.Error
}

func (s *EnrollmentRequestStore) DeleteEnrollmentRequest(orgId uuid.UUID, name string) error {
	condition := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Unscoped().Delete(&condition)
	return result.Error
}
