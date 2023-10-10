package store

import (
	"fmt"
	"log"

	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/internal/service"
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

func (s *EnrollmentRequestStore) CreateEnrollmentRequest(orgId uuid.UUID, resource *model.EnrollmentRequest) (*model.EnrollmentRequest, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	resource.OrgID = orgId
	result := s.db.Create(resource)
	log.Printf("db.Create: %s, %d rows affected, error is %v", resource, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *EnrollmentRequestStore) ListEnrollmentRequests(orgId uuid.UUID) ([]model.EnrollmentRequest, error) {
	condition := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId},
	}
	var enrollmentRequests []model.EnrollmentRequest
	result := s.db.Where(condition).Find(&enrollmentRequests)
	log.Printf("db.Find: %s, %d rows affected, error is %v", enrollmentRequests, result.RowsAffected, result.Error)
	return enrollmentRequests, result.Error
}

func (s *EnrollmentRequestStore) DeleteEnrollmentRequests(orgId uuid.UUID) error {
	condition := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId},
	}
	result := s.db.Delete(&condition)
	return result.Error
}

func (s *EnrollmentRequestStore) GetEnrollmentRequest(orgId uuid.UUID, name string) (*model.EnrollmentRequest, error) {
	enrollmentRequest := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&enrollmentRequest)
	log.Printf("db.Find: %s, %d rows affected, error is %v", name, result.RowsAffected, result.Error)
	return &enrollmentRequest, result.Error
}

func (s *EnrollmentRequestStore) UpdateEnrollmentRequest(orgId uuid.UUID, resource *model.EnrollmentRequest) (*model.EnrollmentRequest, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	enrollmentRequest := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: resource.Name},
	}
	result := s.db.Model(&enrollmentRequest).Updates(map[string]interface{}{
		"spec": model.MakeJSONField(resource.Spec),
	})
	return &enrollmentRequest, result.Error
}

func (s *EnrollmentRequestStore) UpdateEnrollmentRequestStatus(orgId uuid.UUID, resource *model.EnrollmentRequest) (*model.EnrollmentRequest, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	enrollmentRequest := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: resource.Name},
	}
	result := s.db.Model(&enrollmentRequest).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return &enrollmentRequest, result.Error
}

func (s *EnrollmentRequestStore) DeleteEnrollmentRequest(orgId uuid.UUID, name string) error {
	condition := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Delete(&condition)
	return result.Error
}
