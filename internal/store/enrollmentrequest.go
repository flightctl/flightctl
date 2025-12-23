package store

import (
	"context"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type EnrollmentRequest interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, req *api.EnrollmentRequest, callbackEvent EventCallback) (*api.EnrollmentRequest, error)
	CreateWithFromAPI(ctx context.Context, orgId uuid.UUID, req *api.EnrollmentRequest, fromAPI bool, callbackEvent EventCallback) (*api.EnrollmentRequest, error)
	Update(ctx context.Context, orgId uuid.UUID, req *api.EnrollmentRequest, callbackEvent EventCallback) (*api.EnrollmentRequest, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, enrollmentrequest *api.EnrollmentRequest, callbackEvent EventCallback) (*api.EnrollmentRequest, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.EnrollmentRequestList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callbackEvent EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, enrollmentrequest *api.EnrollmentRequest, callbackEvent EventCallback) (*api.EnrollmentRequest, error)
	PrepareEnrollmentRequestsAfterRestore(ctx context.Context) (int64, error)
}

type EnrollmentRequestStore struct {
	dbHandler           *gorm.DB
	log                 logrus.FieldLogger
	genericStore        *GenericStore[*model.EnrollmentRequest, model.EnrollmentRequest, api.EnrollmentRequest, api.EnrollmentRequestList]
	eventCallbackCaller EventCallbackCaller
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
	return &EnrollmentRequestStore{dbHandler: db, log: log, genericStore: genericStore, eventCallbackCaller: CallEventCallback(api.EnrollmentRequestKind, log)}
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

func (s *EnrollmentRequestStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest, eventCallback EventCallback) (*api.EnrollmentRequest, error) {
	er, err := s.genericStore.Create(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), nil, er, true, err)
	return er, err
}

func (s *EnrollmentRequestStore) CreateWithFromAPI(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest, fromAPI bool, eventCallback EventCallback) (*api.EnrollmentRequest, error) {
	// Use CreateOrUpdate with a custom validation callback that ensures create-only behavior
	er, _, _, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, fromAPI, func(ctx context.Context, before, after *api.EnrollmentRequest) error {
		// If there's an existing resource, return an error to enforce create-only behavior
		if before != nil {
			return flterrors.ErrDuplicateName
		}
		return nil
	})
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), nil, er, true, err)
	return er, err
}

func (s *EnrollmentRequestStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest, eventCallback EventCallback) (*api.EnrollmentRequest, error) {
	newEr, oldEr, err := s.genericStore.Update(ctx, orgId, resource, nil, true, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldEr, newEr, false, err)
	return newEr, err
}

func (s *EnrollmentRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest, eventCallback EventCallback) (*api.EnrollmentRequest, bool, error) {
	newEr, oldEr, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, true, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldEr, newEr, created, err)
	return newEr, created, err
}

func (s *EnrollmentRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *EnrollmentRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.EnrollmentRequestList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *EnrollmentRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) error {
	deleted, err := s.genericStore.Delete(ctx, model.EnrollmentRequest{Resource: model.Resource{OrgID: orgId, Name: name}})
	if deleted && eventCallback != nil {
		s.eventCallbackCaller(ctx, eventCallback, orgId, name, nil, nil, false, err)
	}
	return err
}

func (s *EnrollmentRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.EnrollmentRequest, callbackEvent EventCallback) (*api.EnrollmentRequest, error) {
	newEr, err := s.genericStore.UpdateStatus(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, callbackEvent, orgId, lo.FromPtr(resource.Metadata.Name), resource, newEr, false, err)
	return newEr, err
}

// PrepareEnrollmentRequestsAfterRestore sets the awaitingReconnection annotation
// on all non-approved enrollment requests using efficient SQL
func (s *EnrollmentRequestStore) PrepareEnrollmentRequestsAfterRestore(ctx context.Context) (int64, error) {
	db := s.getDB(ctx)

	// Use raw SQL for efficient bulk update that preserves existing annotations
	// and only updates non-approved enrollment requests
	// Check for approval using the status.approval.approved field
	// Handle cases where approval field might be NULL or not exist
	sql := `
		UPDATE enrollment_requests 
		SET 
			annotations = COALESCE(annotations, '{}'::jsonb) || jsonb_build_object($1::text, 'true'),
			resource_version = COALESCE(resource_version, 0) + 1
		WHERE deleted_at IS NULL 
			AND (status->'approval'->>'approved' IS NULL OR status->'approval'->>'approved' != 'true')
			AND (annotations->>$1) IS DISTINCT FROM 'true'
	`

	result := db.Exec(sql, api.DeviceAnnotationAwaitingReconnect)

	if result.Error != nil {
		return 0, ErrorFromGormError(result.Error)
	}

	return result.RowsAffected, nil
}
