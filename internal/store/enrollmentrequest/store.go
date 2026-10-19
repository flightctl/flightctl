package enrollmentrequest

import (
	"context"
	"reflect"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, req *domain.EnrollmentRequest) (*domain.EnrollmentRequest, error)
	Update(ctx context.Context, orgId uuid.UUID, req *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, enrollmentrequest *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, bool, error)
	Mutate(ctx context.Context, orgId uuid.UUID, name string, apply func(*store.Mutation[domain.EnrollmentRequest]) error) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, error)
	List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EnrollmentRequestList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (bool, error)
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
	return s.genericStore.Update(ctx, orgId, resource, nil, nil)
}

func (s *EnrollmentRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, bool, error) {
	return s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, nil)
}

func (s *EnrollmentRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *EnrollmentRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EnrollmentRequestList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *EnrollmentRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (bool, error) {
	return s.genericStore.Delete(ctx, model.EnrollmentRequest{Resource: model.Resource{OrgID: orgId, Name: name}})
}

func (s *EnrollmentRequestStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, apply func(*store.Mutation[domain.EnrollmentRequest]) error) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, bool, error) {
	var incarnation *time.Time
	return s.genericStore.Mutate(ctx, orgId, name, nil, store.MutateHooks[domain.EnrollmentRequest]{
		Wrap: func(current *domain.EnrollmentRequest) store.ResourceMutation[domain.EnrollmentRequest] {
			mutation := &store.Mutation[domain.EnrollmentRequest]{}
			mutation.SetResource(current)
			return mutation
		},
		PersistUpdate: s.persistMutation,
	}, func(mutation store.ResourceMutation[domain.EnrollmentRequest]) error {
		current := mutation.Resource()
		if incarnation != nil {
			if current == nil {
				return flterrors.ErrResourceNotFound
			}
			if current.Metadata.CreationTimestamp == nil || !incarnation.Equal(*current.Metadata.CreationTimestamp) {
				return flterrors.ErrResourceVersionConflict
			}
		} else if current != nil {
			if current.Metadata.CreationTimestamp == nil {
				return flterrors.ErrResourceVersionConflict
			}
			incarnation = lo.ToPtr(*current.Metadata.CreationTimestamp)
		}
		return apply(mutation.(*store.Mutation[domain.EnrollmentRequest]))
	})
}

func (s *EnrollmentRequestStore) persistMutation(ctx context.Context, orgId uuid.UUID, name string, before *domain.EnrollmentRequest, mutation store.ResourceMutation[domain.EnrollmentRequest]) (bool, error) {
	if before.Metadata.CreationTimestamp == nil {
		return false, flterrors.ErrResourceVersionConflict
	}
	record, err := model.NewEnrollmentRequestFromApiResource(before)
	if err != nil {
		return false, err
	}
	record.OrgID = orgId
	proposed, err := model.NewEnrollmentRequestFromApiResource(mutation.Resource())
	if err != nil {
		return false, err
	}
	updates := map[string]interface{}{
		"status":           proposed.Status,
		"resource_version": gorm.Expr("COALESCE(resource_version, 0) + 1"),
	}
	if !proposed.HasSameSpecAs(record) {
		updates["spec"] = proposed.Spec
		updates["generation"] = gorm.Expr("COALESCE(generation, 0) + 1")
	}
	if !reflect.DeepEqual(proposed.Labels, record.Labels) {
		updates["labels"] = proposed.Labels
	}
	if !reflect.DeepEqual(proposed.Annotations, record.Annotations) {
		updates["annotations"] = proposed.Annotations
	}
	result := s.getDB(ctx).Model(record).
		Where("org_id = ? AND name = ? AND created_at = ? AND (resource_version IS NULL OR resource_version = ?)", orgId, name, *before.Metadata.CreationTimestamp, lo.FromPtr(record.ResourceVersion)).
		Clauses(clause.Returning{}).Updates(updates)
	if result.Error != nil {
		return false, store.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected != 1 {
		return true, flterrors.ErrNoRowsUpdated
	}
	updated, err := record.ToApiResource()
	mutation.SetResource(updated)
	return false, err
}
