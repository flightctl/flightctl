package certificatesigningrequest

import (
	"context"

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

	Create(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error)
	Update(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, certificatesigningrequest *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, error)
	List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.CertificateSigningRequestList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (bool, error)
	Mutate(ctx context.Context, orgId uuid.UUID, name string, apply func(*store.Mutation[domain.CertificateSigningRequest]) error) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error)

	UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) error
}

type CertificateSigningRequestStore struct {
	dbHandler    *gorm.DB
	log          logrus.FieldLogger
	genericStore *store.GenericStore[*model.CertificateSigningRequest, model.CertificateSigningRequest, domain.CertificateSigningRequest, domain.CertificateSigningRequestList]
}

// Make sure we conform to the Store interface
var _ Store = (*CertificateSigningRequestStore)(nil)

func NewCertificateSigningRequestStore(db *gorm.DB, log logrus.FieldLogger) Store {
	genericStore := store.NewGenericStore[*model.CertificateSigningRequest, model.CertificateSigningRequest, domain.CertificateSigningRequest, domain.CertificateSigningRequestList](
		db,
		log,
		model.NewCertificateSigningRequestFromApiResource,
		(*model.CertificateSigningRequest).ToApiResource,
		model.CertificateSigningRequestsToApiResource,
	)
	return &CertificateSigningRequestStore{dbHandler: db, log: log, genericStore: genericStore}
}

func (s *CertificateSigningRequestStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *CertificateSigningRequestStore) InitialMigration(ctx context.Context) error {
	return InitialMigration(ctx, s.dbHandler)
}

func InitialMigration(ctx context.Context, db *gorm.DB) error {
	db = db.WithContext(ctx)

	if err := db.AutoMigrate(&model.CertificateSigningRequest{}); err != nil {
		return err
	}
	if !db.Migrator().HasIndex(&model.CertificateSigningRequest{}, "idx_csr_org_certificate_digest") {
		if err := db.Exec("CREATE INDEX idx_csr_org_certificate_digest ON certificate_signing_requests (org_id, certificate_digest)").Error; err != nil {
			return err
		}
	}

	// Create GIN index for CertificateSigningRequest labels
	if !db.Migrator().HasIndex(&model.CertificateSigningRequest{}, "idx_csr_labels") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_csr_labels ON certificate_signing_requests USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.CertificateSigningRequest{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for CertificateSigningRequest annotations
	if !db.Migrator().HasIndex(&model.CertificateSigningRequest{}, "idx_csr_annotations") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_csr_annotations ON certificate_signing_requests USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.CertificateSigningRequest{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

// Warning: this is a user-facing function and will set the Status to nil
func (s *CertificateSigningRequestStore) Create(ctx context.Context, orgId uuid.UUID, resource *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	return s.genericStore.Create(ctx, orgId, resource)
}

// Warning: this is a user-facing function and will set the Status to nil
func (s *CertificateSigningRequestStore) Update(ctx context.Context, orgId uuid.UUID, resource *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, error) {
	return s.genericStore.Update(ctx, orgId, resource, nil, nil)
}

func (s *CertificateSigningRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	return s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, nil)
}

func (s *CertificateSigningRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *CertificateSigningRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.CertificateSigningRequestList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *CertificateSigningRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (bool, error) {
	return s.genericStore.Delete(ctx, model.CertificateSigningRequest{Resource: model.Resource{OrgID: orgId, Name: name}})
}

func (s *CertificateSigningRequestStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, apply func(*store.Mutation[domain.CertificateSigningRequest]) error) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	return s.genericStore.Mutate(ctx, orgId, name, nil, store.MutateHooks[domain.CertificateSigningRequest]{
		Wrap: func(current *domain.CertificateSigningRequest) store.ResourceMutation[domain.CertificateSigningRequest] {
			mutation := &store.Mutation[domain.CertificateSigningRequest]{}
			mutation.SetResource(current)
			return mutation
		},
		PersistUpdate: s.persistStatus,
	}, func(mutation store.ResourceMutation[domain.CertificateSigningRequest]) error {
		return apply(mutation.(*store.Mutation[domain.CertificateSigningRequest]))
	})
}

func (s *CertificateSigningRequestStore) persistStatus(ctx context.Context, orgId uuid.UUID, name string, before *domain.CertificateSigningRequest, mutation store.ResourceMutation[domain.CertificateSigningRequest]) (bool, error) {
	if before.Metadata.CreationTimestamp == nil {
		return false, flterrors.ErrResourceVersionConflict
	}
	record, err := model.NewCertificateSigningRequestFromApiResource(before)
	if err != nil {
		return false, err
	}
	record.OrgID = orgId
	result := s.getDB(ctx).Model(record).
		Where("org_id = ? AND name = ? AND created_at = ? AND (resource_version IS NULL OR resource_version = ?)", orgId, name, *before.Metadata.CreationTimestamp, lo.FromPtr(record.ResourceVersion)).
		Clauses(clause.Returning{}).Updates(map[string]interface{}{
		"status": model.MakeJSONField(*mutation.Resource().Status), "resource_version": gorm.Expr("COALESCE(resource_version, 0) + 1"),
		"certificate_digest": model.CertificateSigningRequestCertificateDigest(mutation.Resource().Status.Certificate),
	})
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

func (s *CertificateSigningRequestStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) error {
	_, _, _, err := s.Mutate(ctx, orgId, name, func(mutation *store.Mutation[domain.CertificateSigningRequest]) error {
		if err := mutation.RequireExisting(); err != nil {
			return err
		}
		current := mutation.Resource()
		if current.Status == nil {
			current.Status = &domain.CertificateSigningRequestStatus{}
		}
		changed := false
		for _, condition := range conditions {
			if domain.SetStatusCondition(&current.Status.Conditions, condition) {
				changed = true
			}
		}
		if !changed {
			return store.ErrMutateSkipWrite
		}
		return nil
	})
	return err
}
