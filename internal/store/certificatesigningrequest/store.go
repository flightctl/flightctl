package certificatesigningrequest

import (
	"context"
	"reflect"

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
	Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.CertificateSigningRequest, apply CertificateSigningRequestApplyFunc) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error)

	UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) error
}

type CertificateSigningRequestStore struct {
	dbHandler    *gorm.DB
	log          logrus.FieldLogger
	genericStore *store.GenericStore[*model.CertificateSigningRequest, model.CertificateSigningRequest, domain.CertificateSigningRequest, domain.CertificateSigningRequestList]
}

// CertificateSigningRequestMutation is the unit apply mutates. Handlers decide all field changes.
// CertificateSigningRequest is nil on the create path until apply assigns it.
type CertificateSigningRequestMutation struct {
	CertificateSigningRequest *domain.CertificateSigningRequest
}

func (m *CertificateSigningRequestMutation) Resource() *domain.CertificateSigningRequest {
	return m.CertificateSigningRequest
}

func (m *CertificateSigningRequestMutation) SetResource(resource *domain.CertificateSigningRequest) {
	m.CertificateSigningRequest = resource
}

func (m *CertificateSigningRequestMutation) Clone() (store.ResourceMutation[domain.CertificateSigningRequest], error) {
	cloned, err := store.CloneJSON(m.CertificateSigningRequest)
	if err != nil {
		return nil, err
	}
	return &CertificateSigningRequestMutation{CertificateSigningRequest: cloned}, nil
}

// RequireExisting returns ErrResourceNotFound when CertificateSigningRequest is nil (create path).
func (m *CertificateSigningRequestMutation) RequireExisting() error {
	if m.CertificateSigningRequest == nil {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

// CertificateSigningRequestApplyFunc mutates m in place on every Mutate attempt.
type CertificateSigningRequestApplyFunc func(m *CertificateSigningRequestMutation) error

var _ store.ResourceMutation[domain.CertificateSigningRequest] = (*CertificateSigningRequestMutation)(nil)

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
	db := s.getDB(ctx)

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

// Mutate applies an atomic create-or-update; apply decides which resource fields change.
func (s *CertificateSigningRequestStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.CertificateSigningRequest, apply CertificateSigningRequestApplyFunc) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	if previous != nil && lo.FromPtr(previous.Metadata.Name) != name {
		previous = nil
	}
	hooks := store.MutateHooks[domain.CertificateSigningRequest]{
		Wrap: func(current *domain.CertificateSigningRequest) store.ResourceMutation[domain.CertificateSigningRequest] {
			return &CertificateSigningRequestMutation{CertificateSigningRequest: current}
		},
		PersistCreate: func(ctx context.Context, orgId uuid.UUID, mutation store.ResourceMutation[domain.CertificateSigningRequest]) (*domain.CertificateSigningRequest, error) {
			return s.Create(ctx, orgId, mutation.Resource())
		},
		PersistUpdate: s.persistMutation,
	}
	return s.genericStore.Mutate(ctx, orgId, name, previous, hooks, func(mutation store.ResourceMutation[domain.CertificateSigningRequest]) error {
		return apply(mutation.(*CertificateSigningRequestMutation))
	})
}

func (s *CertificateSigningRequestStore) persistMutation(ctx context.Context, orgId uuid.UUID, name string, before *domain.CertificateSigningRequest, mutation store.ResourceMutation[domain.CertificateSigningRequest]) (bool, error) {
	if mutation.Resource() == nil || mutation.Resource().Status == nil {
		return false, flterrors.ErrResourceIsNil
	}
	if before.Metadata.CreationTimestamp == nil {
		return false, flterrors.ErrResourceVersionConflict
	}
	record, err := model.NewCertificateSigningRequestFromApiResource(before)
	if err != nil {
		return false, err
	}
	record.OrgID = orgId
	proposed, err := model.NewCertificateSigningRequestFromApiResource(mutation.Resource())
	if err != nil {
		return false, err
	}
	updates := map[string]interface{}{
		"status":             proposed.Status,
		"certificate_digest": proposed.CertificateDigest,
		"resource_version":   gorm.Expr("COALESCE(resource_version, 0) + 1"),
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

func (s *CertificateSigningRequestStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) error {
	_, _, _, err := s.Mutate(ctx, orgId, name, nil, func(mutation *CertificateSigningRequestMutation) error {
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
