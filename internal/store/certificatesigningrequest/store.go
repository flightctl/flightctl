package certificatesigningrequest

import (
	"context"
	"errors"
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
	db := s.getDB(ctx)

	if err := db.AutoMigrate(&model.CertificateSigningRequest{}); err != nil {
		return err
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
	deleted := false
	err := s.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.CertificateSigningRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND name = ?", orgId, name).Take(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		query := tx
		if !s.isEnrollmentCredential(&existing) ||
			existing.Status == nil || existing.Status.Data.Certificate == nil || len(*existing.Status.Data.Certificate) == 0 {
			query = tx.Unscoped()
		}
		result := query.Delete(&existing)
		deleted = result.RowsAffected > 0
		return result.Error
	})
	return deleted, store.ErrorFromGormError(err)
}

func (s *CertificateSigningRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	if resource == nil || resource.Metadata.Name == nil || resource.Status == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	var updated *domain.CertificateSigningRequest
	err := s.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.CertificateSigningRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND name = ?", orgId, *resource.Metadata.Name).Take(&existing).Error; err != nil {
			return err
		}
		candidate, err := model.NewCertificateSigningRequestFromApiResource(resource)
		if err != nil {
			return err
		}
		enrollmentCredential := s.isEnrollmentCredential(&existing) || s.isEnrollmentCredential(candidate)
		if enrollmentCredential && resource.Status.Certificate != nil && len(*resource.Status.Certificate) > 0 {
			if !candidate.HasSameSpecAs(&existing) {
				return flterrors.ErrResourceVersionConflict
			}
		}
		status := *resource.Status
		if enrollmentCredential && existing.Status != nil && existing.Status.Data.Certificate != nil && len(*existing.Status.Data.Certificate) > 0 {
			status.Certificate = existing.Status.Data.Certificate
			status.IssuedAt = existing.Status.Data.IssuedAt
		}
		if enrollmentCredential && (status.Certificate == nil || len(*status.Certificate) == 0) {
			status.IssuedAt = nil
		}
		query := tx.Model(&existing)
		if enrollmentCredential && resource.Metadata.CreationTimestamp != nil {
			query = query.Where("created_at = ?", *resource.Metadata.CreationTimestamp)
		} else if enrollmentCredential && resource.Status.Certificate != nil && len(*resource.Status.Certificate) > 0 {
			return flterrors.ErrResourceVersionConflict
		}
		result := query.Clauses(clause.Returning{}).Updates(map[string]interface{}{
			"status": model.MakeJSONField(status), "resource_version": gorm.Expr("resource_version + 1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return flterrors.ErrResourceVersionConflict
		}
		updated, err = existing.ToApiResource()
		return err
	})
	return updated, store.ErrorFromGormError(err)
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
		changed = domain.SetStatusCondition(&existingRecord.Status.Data.Conditions, condition)
	}
	if !changed {
		return false, nil
	}

	result = s.getDB(ctx).Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"status":           existingRecord.Status,
		"resource_version": gorm.Expr("resource_version + 1"),
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
