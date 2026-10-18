package certificatesigningrequest

import (
	"context"
	"strings"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
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
	FindNameByCertificate(ctx context.Context, orgId uuid.UUID, leafDER []byte) (*string, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (bool, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, certificatesigningrequest *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error)

	UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) error
}

type CertificateSigningRequestStore struct {
	dbHandler            *gorm.DB
	log                  logrus.FieldLogger
	enrollmentSignerName string
	genericStore         *store.GenericStore[*model.CertificateSigningRequest, model.CertificateSigningRequest, domain.CertificateSigningRequest, domain.CertificateSigningRequestList]
}

// Make sure we conform to the Store interface
var _ Store = (*CertificateSigningRequestStore)(nil)

func NewCertificateSigningRequestStore(db *gorm.DB, log logrus.FieldLogger, enrollmentSignerName string) Store {
	genericStore := store.NewGenericStore[*model.CertificateSigningRequest, model.CertificateSigningRequest, domain.CertificateSigningRequest, domain.CertificateSigningRequestList](
		db,
		log,
		model.NewCertificateSigningRequestFromApiResource,
		(*model.CertificateSigningRequest).ToApiResource,
		model.CertificateSigningRequestsToApiResource,
	)
	return &CertificateSigningRequestStore{dbHandler: db, log: log, enrollmentSignerName: enrollmentSignerName, genericStore: genericStore}
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

func (s *CertificateSigningRequestStore) isEnrollmentCredential(resource *model.CertificateSigningRequest) bool {
	if s.enrollmentSignerName == "" {
		return false
	}
	if resource.Spec != nil && resource.Spec.Data.SignerName == s.enrollmentSignerName {
		return true
	}
	if resource.Status == nil || resource.Status.Data.Certificate == nil {
		return false
	}
	leaf, err := fcrypto.ParseCertificatePEM(*resource.Status.Data.Certificate)
	if err != nil {
		return false
	}
	name, err := signer.GetSignerNameExtension(leaf)
	return err == nil && name == s.enrollmentSignerName
}

func (s *CertificateSigningRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (bool, error) {
	return s.genericStore.Delete(ctx, model.CertificateSigningRequest{Resource: model.Resource{OrgID: orgId, Name: name}})
}

func (s *CertificateSigningRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	if resource == nil || resource.Metadata.Name == nil || resource.Status == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	candidate, err := store.CloneJSON(resource)
	if err != nil {
		return nil, err
	}
	updated, _, _, err := s.genericStore.Mutate(ctx, orgId, *resource.Metadata.Name, nil,
		store.MutateHooks[domain.CertificateSigningRequest]{
			Wrap: func(current *domain.CertificateSigningRequest) store.ResourceMutation[domain.CertificateSigningRequest] {
				mutation := &store.Mutation[domain.CertificateSigningRequest]{}
				mutation.SetResource(current)
				return mutation
			},
			PersistUpdate: s.persistStatus,
		}, func(mutation store.ResourceMutation[domain.CertificateSigningRequest]) error {
			current := mutation.Resource()
			if current == nil {
				return flterrors.ErrResourceNotFound
			}
			existing, err := model.NewCertificateSigningRequestFromApiResource(current)
			if err != nil {
				return err
			}
			proposed, err := model.NewCertificateSigningRequestFromApiResource(candidate)
			if err != nil {
				return err
			}
			enrollment := s.isEnrollmentCredential(existing) || s.isEnrollmentCredential(proposed)
			issuing := candidate.Status.Certificate != nil && len(*candidate.Status.Certificate) > 0
			if enrollment {
				if issuing && (!proposed.HasSameSpecAs(existing) || candidate.Metadata.CreationTimestamp == nil) {
					return flterrors.ErrResourceVersionConflict
				}
				if candidate.Metadata.CreationTimestamp != nil && (current.Metadata.CreationTimestamp == nil || !candidate.Metadata.CreationTimestamp.Equal(*current.Metadata.CreationTimestamp)) {
					return flterrors.ErrResourceVersionConflict
				}
			}
			status := *candidate.Status
			if enrollment && current.Status != nil && current.Status.Certificate != nil && len(*current.Status.Certificate) > 0 {
				status.Certificate = current.Status.Certificate
				status.IssuedAt = current.Status.IssuedAt
			}
			if !enrollment || status.Certificate == nil || len(*status.Certificate) == 0 {
				status.IssuedAt = nil
			}
			current.Status = &status
			return nil
		})
	return updated, store.ErrorFromGormError(err)
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

func (s *CertificateSigningRequestStore) updateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) (bool, error) {
	existingRecord := model.CertificateSigningRequest{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.getDB(ctx).Take(&existingRecord)
	if result.Error != nil {
		return false, store.ErrorFromGormError(result.Error)
	}

	if existingRecord.Status == nil {
		existingRecord.Status = model.MakeJSONField(domain.CertificateSigningRequestStatus{})
	}
	if existingRecord.Status.Data.Conditions == nil {
		existingRecord.Status.Data.Conditions = []domain.Condition{}
	}
	changed := false
	for _, condition := range conditions {
		if domain.SetStatusCondition(&existingRecord.Status.Data.Conditions, condition) {
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	if !s.isEnrollmentCredential(&existingRecord) {
		existingRecord.Status.Data.IssuedAt = nil
	}

	result = s.getDB(ctx).Model(existingRecord).Where("created_at = ? AND resource_version = ?", existingRecord.CreatedAt, lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"status":             existingRecord.Status,
		"certificate_digest": model.CertificateSigningRequestCertificateDigest(existingRecord.Status.Data.Certificate),
		"resource_version":   gorm.Expr("resource_version + 1"),
	})
	err := store.ErrorFromGormError(result.Error)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *CertificateSigningRequestStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) error {
	return retryUpdate(func() (bool, error) {
		return s.updateConditions(ctx, orgId, name, conditions)
	})
}

// retryIterations and retryUpdate mirror the unexported helpers of the same
// name in internal/store (internal/store/common.go), which are not exported
// for cross-package reuse. Duplicated here since UpdateConditions needs
// identical optimistic-concurrency retry behavior within this package.
const retryIterations = 10

func retryUpdate(fn func() (bool, error)) error {
	var (
		retry bool
		err   error
	)
	i := 0
	for retry, err = fn(); retry && i < retryIterations; retry, err = fn() {
		i++
	}
	return err
}
