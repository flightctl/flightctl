package enrollmentapprovalpolicy

import (
	"context"
	"errors"
	"fmt"

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

var ErrNameReserved = fmt.Errorf("policy name is reserved: %w", flterrors.ErrDuplicateName)

type ApplyFunc = store.ApplyFunc[domain.EnrollmentApprovalPolicy]

type Store interface {
	InitialMigration(context.Context) error
	Get(context.Context, uuid.UUID, string) (*domain.EnrollmentApprovalPolicy, error)
	List(context.Context, uuid.UUID, store.ListParams) (*domain.EnrollmentApprovalPolicyList, error)
	WithTransaction(context.Context, func(context.Context) error) error
	GetCredential(context.Context, uuid.UUID, string) (*domain.CertificateSigningRequest, error)
	Mutate(context.Context, uuid.UUID, string, ApplyFunc) (*domain.EnrollmentApprovalPolicy, *domain.EnrollmentApprovalPolicy, bool, error)
	Delete(context.Context, uuid.UUID, string) error
}

type PolicyStore struct {
	db      *gorm.DB
	generic *store.GenericStore[*model.EnrollmentApprovalPolicy, model.EnrollmentApprovalPolicy, domain.EnrollmentApprovalPolicy, domain.EnrollmentApprovalPolicyList]
}

func NewStore(db *gorm.DB, log logrus.FieldLogger) (Store, error) {
	if db == nil {
		return nil, fmt.Errorf("enrollment approval policy database is required")
	}
	if lo.IsNil(log) {
		return nil, fmt.Errorf("enrollment approval policy logger is required")
	}
	return &PolicyStore{db: db, generic: store.NewGenericStore[*model.EnrollmentApprovalPolicy, model.EnrollmentApprovalPolicy, domain.EnrollmentApprovalPolicy, domain.EnrollmentApprovalPolicyList](db, log, model.NewEnrollmentApprovalPolicyFromApiResource, (*model.EnrollmentApprovalPolicy).ToApiResource, model.EnrollmentApprovalPoliciesToApiResource)}, nil
}

func (policies *PolicyStore) Get(ctx context.Context, orgID uuid.UUID, name string) (*domain.EnrollmentApprovalPolicy, error) {
	return policies.generic.Get(ctx, orgID, name)
}

func (policies *PolicyStore) List(ctx context.Context, orgID uuid.UUID, params store.ListParams) (*domain.EnrollmentApprovalPolicyList, error) {
	return policies.generic.List(ctx, orgID, params)
}

func (policies *PolicyStore) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	if store.InTransaction(ctx) {
		return fn(ctx)
	}
	return store.WithTransaction(ctx, policies.db, fn)
}

func (policies *PolicyStore) GetCredential(ctx context.Context, orgID uuid.UUID, certificateName string) (*domain.CertificateSigningRequest, error) {
	var credential model.CertificateSigningRequest
	err := store.DB(ctx, policies.db).Where("org_id = ? AND name = ? AND spec IS NOT NULL", orgID, certificateName).First(&credential).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, persistenceError(err)
	}
	return credential.ToApiResource()
}

func (policies *PolicyStore) Mutate(ctx context.Context, orgID uuid.UUID, name string, apply ApplyFunc) (*domain.EnrollmentApprovalPolicy, *domain.EnrollmentApprovalPolicy, bool, error) {
	if apply == nil {
		return nil, nil, false, fmt.Errorf("policy mutation requires apply")
	}
	var persistedBinding string
	hooks := store.MutateHooks[domain.EnrollmentApprovalPolicy]{
		MaxRetries:  lo.ToPtr(2),
		RetryAborts: true,
		Load: func(ctx context.Context, orgID uuid.UUID, name string) (*domain.EnrollmentApprovalPolicy, error) {
			record, err := loadPolicy(store.DB(ctx, policies.db), orgID, name)
			if err != nil || record == nil {
				return nil, err
			}
			persistedBinding = record.CertificateName
			return record.ToApiResource()
		},
		Wrap: func(current *domain.EnrollmentApprovalPolicy) store.ResourceMutation[domain.EnrollmentApprovalPolicy] {
			mutation := &store.Mutation[domain.EnrollmentApprovalPolicy]{}
			mutation.SetResource(current)
			return mutation
		},
		PersistCreate: func(ctx context.Context, orgID uuid.UUID, mutation store.ResourceMutation[domain.EnrollmentApprovalPolicy]) (*domain.EnrollmentApprovalPolicy, error) {
			candidate := mutation.Resource()
			record, err := model.NewEnrollmentApprovalPolicyFromApiResource(candidate)
			if err != nil {
				return nil, err
			}
			record.OrgID = orgID
			record.Status = model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{})
			record.ResourceVersion, record.Generation = lo.ToPtr(int64(1)), lo.ToPtr(int64(1))
			if err := store.DB(ctx, policies.db).Clauses(clause.Returning{}).Create(record).Error; err != nil {
				return nil, persistenceError(err)
			}
			return record.ToApiResource()
		},
		PersistUpdate: policies.persistUpdate,
	}
	return policies.generic.Mutate(ctx, orgID, name, nil, hooks, func(m store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
		current := m.Resource()
		currentBindingMatches := current == nil || current.Spec.CertificateName == persistedBinding
		err := apply(m)
		if err != nil && !errors.Is(err, store.ErrMutateSkipWrite) {
			return err
		}
		candidate := m.Resource()
		if !currentBindingMatches || (candidate != nil && (lo.FromPtr(candidate.Metadata.Name) != name || (current != nil && candidate.Spec.CertificateName != persistedBinding))) {
			return flterrors.ErrImmutableCertificateBinding
		}
		if err == nil && candidate == nil {
			return flterrors.ErrImmutableCertificateBinding
		}
		return err
	})
}

func loadPolicy(db *gorm.DB, orgID uuid.UUID, name string) (*model.EnrollmentApprovalPolicy, error) {
	var record model.EnrollmentApprovalPolicy
	err := db.Unscoped().Where("org_id = ? AND name = ?", orgID, name).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, persistenceError(err)
	}
	if record.DeletedAt.Valid {
		return nil, ErrNameReserved
	}
	return &record, nil
}

func (policies *PolicyStore) persistUpdate(ctx context.Context, orgID uuid.UUID, name string, before *domain.EnrollmentApprovalPolicy, mutation store.ResourceMutation[domain.EnrollmentApprovalPolicy]) (bool, error) {
	candidate := mutation.Resource()
	if candidate.Metadata.ResourceVersion == nil || lo.FromPtr(candidate.Metadata.ResourceVersion) != lo.FromPtr(before.Metadata.ResourceVersion) {
		return false, flterrors.ErrResourceVersionConflict
	}
	record, err := model.NewEnrollmentApprovalPolicyFromApiResource(candidate)
	if err != nil {
		return false, err
	}
	previous, err := model.NewEnrollmentApprovalPolicyFromApiResource(before)
	if err != nil {
		return false, err
	}
	record.OrgID = orgID
	record.ResourceVersion = lo.ToPtr(lo.FromPtr(previous.ResourceVersion) + 1)
	record.Generation = previous.Generation
	if !record.HasSameSpecAs(previous) {
		record.Generation = lo.ToPtr(lo.FromPtr(before.Metadata.Generation) + 1)
	}
	// RETURNING populates server-owned status without including it in the update.
	result := store.DB(ctx, policies.db).Model(record).Where("org_id = ? AND name = ? AND certificate_name = ? AND resource_version = ?", orgID, name, previous.CertificateName, previous.ResourceVersion).Clauses(clause.Returning{}).Select("spec", "labels", "annotations", "resource_version", "generation", "updated_at").Updates(record)
	if result.Error != nil {
		return false, persistenceError(result.Error)
	}
	if result.RowsAffected != 1 {
		return true, flterrors.ErrResourceVersionConflict
	}
	updated, err := record.ToApiResource()
	if err != nil {
		return false, err
	}
	mutation.SetResource(updated)
	return false, nil
}

// Delete retains policy history, accounting and the reserved name.
func (policies *PolicyStore) Delete(ctx context.Context, orgID uuid.UUID, name string) error {
	result := store.DB(ctx, policies.db).
		Where("org_id = ? AND name = ?", orgID, name).
		Delete(&model.EnrollmentApprovalPolicy{})
	if result.Error != nil {
		return persistenceError(result.Error)
	}
	if result.RowsAffected != 1 {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

func persistenceError(err error) error {
	// An unrelated FK violation (for example the organization FK) is a
	// persistence error. The named credential FK is classified separately.
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		return err
	}
	mapped := store.ErrorFromGormError(err)
	if mapped == err {
		return err
	}
	return errors.Join(mapped, err)
}
