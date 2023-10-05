package store

import (
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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

func (s *EnrollmentRequestStore) CreateEnrollmentRequest(orgId uuid.UUID, name string) (model.EnrollmentRequest, error) {
	resource := model.EnrollmentRequest{
		Resource: model.Resource{
			OrgID: orgId,
			Name:  name,
		},
		Spec:   model.MakeJSONField(api.EnrollmentRequestSpec{}),
		Status: model.MakeJSONField(api.EnrollmentRequestStatus{}),
	}
	result := s.db.Create(&resource)
	log.Printf("db.Create: %s, %d rows affected, error is %v", resource, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *EnrollmentRequestStore) ListEnrollmentRequests(orgId uuid.UUID) ([]model.EnrollmentRequest, error) {
	condition := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId},
	}

	var enrollmentrequests []model.EnrollmentRequest
	result := s.db.Where(condition).Find(&enrollmentrequests)
	log.Printf("db.Find: %s, %d rows affected, error is %v", enrollmentrequests, result.RowsAffected, result.Error)
	return enrollmentrequests, result.Error
}

func (s *EnrollmentRequestStore) GetEnrollmentRequest(orgId uuid.UUID, name string) (model.EnrollmentRequest, error) {
	enrollmentrequest := model.EnrollmentRequest{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&enrollmentrequest)
	log.Printf("db.Find: %s, %d rows affected, error is %v", enrollmentrequest, result.RowsAffected, result.Error)
	return enrollmentrequest, result.Error
}

func (s *EnrollmentRequestStore) WriteEnrollmentRequestSpec(orgId uuid.UUID, name string, spec api.EnrollmentRequestSpec) error {
	return nil
}
func (s *EnrollmentRequestStore) WriteEnrollmentRequestStatus(orgId uuid.UUID, name string, status api.EnrollmentRequestStatus) error {
	return nil
}
func (s *EnrollmentRequestStore) DeleteEnrollmentRequests(orgId uuid.UUID) error {
	return nil
}
func (s *EnrollmentRequestStore) DeleteEnrollmentRequest(orgId uuid.UUID, name string) error {
	return nil
}
