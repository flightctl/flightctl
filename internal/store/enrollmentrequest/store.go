package enrollmentrequest

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Store interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, req *domain.EnrollmentRequest) (*domain.EnrollmentRequest, error)
	Update(ctx context.Context, orgId uuid.UUID, req *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, enrollmentrequest *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, error)
	List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EnrollmentRequestList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, enrollmentrequest *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, error)
}

type EnrollmentRequestStore struct {
	dbHandler    *gorm.DB
	log          logrus.FieldLogger
	genericStore *store.GenericStore[*model.EnrollmentRequest, model.EnrollmentRequest, domain.EnrollmentRequest, domain.EnrollmentRequestList]
}

// Make sure we conform to the Store interface
var _ Store = (*EnrollmentRequestStore)(nil)

func NewEnrollmentRequestStore(db *gorm.DB, log logrus.FieldLogger) Store {
	genericStore := store.NewGenericStore[*model.EnrollmentRequest, model.EnrollmentRequest, domain.EnrollmentRequest, domain.EnrollmentRequestList](
		db,
		log,
		model.NewEnrollmentRequestFromApiResource,
		(*model.EnrollmentRequest).ToApiResource,
		model.EnrollmentRequestsToApiResource,
	)
	return &EnrollmentRequestStore{dbHandler: db, log: log, genericStore: genericStore}
}

func (s *EnrollmentRequestStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *EnrollmentRequestStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)

	if err := db.AutoMigrate(&model.EnrollmentRequest{}); err != nil {
		return err
	}

	// Create GIN index for EnrollmentRequest labels
	if !db.Migrator().HasIndex(&model.EnrollmentRequest{}, "idx_enrollment_requests_labels") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_enrollment_requests_labels ON enrollment_requests USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.EnrollmentRequest{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for EnrollmentRequest annotations
	if !db.Migrator().HasIndex(&model.EnrollmentRequest{}, "idx_enrollment_requests_annotations") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_enrollment_requests_annotations ON enrollment_requests USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.EnrollmentRequest{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *EnrollmentRequestStore) Create(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest) (*domain.EnrollmentRequest, error) {
	return s.genericStore.Create(ctx, orgId, resource)
}

func (s *EnrollmentRequestStore) Update(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, error) {
	return s.genericStore.Update(ctx, orgId, resource, nil)
}

func (s *EnrollmentRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, bool, error) {
	return s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil)
}

func (s *EnrollmentRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *EnrollmentRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EnrollmentRequestList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *EnrollmentRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	_, err := s.genericStore.Delete(ctx, model.EnrollmentRequest{Resource: model.Resource{OrgID: orgId, Name: name}})
	return err
}

func (s *EnrollmentRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, error) {
	newEr, err := s.genericStore.UpdateStatus(ctx, orgId, resource)
	return newEr, resource, err
}
