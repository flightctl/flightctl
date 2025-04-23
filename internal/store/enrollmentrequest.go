package store

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type EnrollmentRequest interface {
	InitialMigration() error

	Create(ctx context.Context, orgId uuid.UUID, req *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
	Update(ctx context.Context, orgId uuid.UUID, req *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, enrollmentrequest *api.EnrollmentRequest) (*api.EnrollmentRequest, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.EnrollmentRequestList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string) error
	DeleteAll(ctx context.Context, orgId uuid.UUID) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, enrollmentrequest *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
}

type EnrollmentRequestStore struct {
	db           *gorm.DB
	log          logrus.FieldLogger
	genericStore *GenericStore[*model.EnrollmentRequest, model.EnrollmentRequest, api.EnrollmentRequest, api.EnrollmentRequestList]
}

// Make sure we conform to EnrollmentRequest interface
var _ EnrollmentRequest = (*EnrollmentRequestStore)(nil)

func NewEnrollmentRequest(db *gorm.DB, log logrus.FieldLogger) EnrollmentRequest {
	genericStore := NewGenericStore[*model.EnrollmentRequest, model.EnrollmentRequest, api.EnrollmentRequest, api.EnrollmentRequestList](
		db,
		log,
		model.NewEnrollmentRequestFromApiResource,
		(*model.EnrollmentRequest).ToApiResource,
		model.EnrollmentRequestsToApiResource,
	)
	return &EnrollmentRequestStore{db: db, log: log, genericStore: genericStore}
}

func (s *EnrollmentRequestStore) InitialMigration() error {
	if err := s.db.AutoMigrate(&model.EnrollmentRequest{}); err != nil {
		return err
	}

	// Create GIN index for EnrollmentRequest labels
	if !s.db.Migrator().HasIndex(&model.EnrollmentRequest{}, "idx_enrollment_requests_labels") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_enrollment_requests_labels ON enrollment_requests USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.EnrollmentRequest{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for EnrollmentRequest annotations
	if !s.db.Migrator().HasIndex(&model.EnrollmentRequest{}, "idx_enrollment_requests_annotations") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_enrollment_requests_annotations ON enrollment_requests USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.EnrollmentRequest{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *EnrollmentRequestStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, error) {
	return s.genericStore.Create(ctx, orgId, resource, nil)
}

func (s *EnrollmentRequestStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, error) {
	return s.genericStore.Update(ctx, orgId, resource, nil, true, nil, nil)
}

func (s *EnrollmentRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, bool, error) {
	return s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, true, nil, nil)
}

func (s *EnrollmentRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *EnrollmentRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.EnrollmentRequestList, error) {
	return s.genericStore.List(ctx, orgId, listParams, nil)
}

func (s *EnrollmentRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	return s.genericStore.Delete(ctx, model.EnrollmentRequest{Resource: model.Resource{OrgID: orgId, Name: name}}, nil)
}

func (s *EnrollmentRequestStore) DeleteAll(ctx context.Context, orgId uuid.UUID) error {
	return s.genericStore.DeleteAll(ctx, orgId, nil)
}

func (s *EnrollmentRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest) (*api.EnrollmentRequest, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}
