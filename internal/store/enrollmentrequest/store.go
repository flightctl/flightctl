package enrollmentrequest

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Store interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, req *domain.EnrollmentRequest, callbackEvent store.EventCallback) (*domain.EnrollmentRequest, error)
	Update(ctx context.Context, orgId uuid.UUID, req *domain.EnrollmentRequest, callbackEvent store.EventCallback) (*domain.EnrollmentRequest, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, enrollmentrequest *domain.EnrollmentRequest, callbackEvent store.EventCallback) (*domain.EnrollmentRequest, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, error)
	List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EnrollmentRequestList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callbackEvent store.EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, enrollmentrequest *domain.EnrollmentRequest, callbackEvent store.EventCallback) (*domain.EnrollmentRequest, error)
}

type EnrollmentRequestStore struct {
	dbHandler           *gorm.DB
	log                 logrus.FieldLogger
	genericStore        *store.GenericStore[*model.EnrollmentRequest, model.EnrollmentRequest, domain.EnrollmentRequest, domain.EnrollmentRequestList]
	eventCallbackCaller store.EventCallbackCaller
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
	return &EnrollmentRequestStore{dbHandler: db, log: log, genericStore: genericStore, eventCallbackCaller: store.CallEventCallback(domain.EnrollmentRequestKind, log)}
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

func (s *EnrollmentRequestStore) Create(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest, eventCallback store.EventCallback) (*domain.EnrollmentRequest, error) {
	er, err := s.genericStore.Create(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), nil, er, true, err)
	return er, err
}

func (s *EnrollmentRequestStore) Update(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest, eventCallback store.EventCallback) (*domain.EnrollmentRequest, error) {
	newEr, oldEr, err := s.genericStore.Update(ctx, orgId, resource, nil, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldEr, newEr, false, err)
	return newEr, err
}

func (s *EnrollmentRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest, eventCallback store.EventCallback) (*domain.EnrollmentRequest, bool, error) {
	newEr, oldEr, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldEr, newEr, created, err)
	return newEr, created, err
}

func (s *EnrollmentRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *EnrollmentRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EnrollmentRequestList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *EnrollmentRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error {
	deleted, err := s.genericStore.Delete(ctx, model.EnrollmentRequest{Resource: model.Resource{OrgID: orgId, Name: name}})
	if deleted && eventCallback != nil {
		s.eventCallbackCaller(ctx, eventCallback, orgId, name, nil, nil, false, err)
	}
	return err
}

func (s *EnrollmentRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest, callbackEvent store.EventCallback) (*domain.EnrollmentRequest, error) {
	newEr, err := s.genericStore.UpdateStatus(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), resource, newEr, false, err)
	return newEr, err
}
