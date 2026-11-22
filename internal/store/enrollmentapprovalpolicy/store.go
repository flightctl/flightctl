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
	WithPolicySetTransaction(context.Context, uuid.UUID, string, string, func(context.Context, *domain.CertificateSigningRequest, []domain.EnrollmentApprovalPolicy) error) error
	Mutate(context.Context, uuid.UUID, string, ApplyFunc) (*domain.EnrollmentApprovalPolicy, *domain.EnrollmentApprovalPolicy, bool, error)
	Delete(context.Context, uuid.UUID, string) error
}

type PolicyStore struct {
	db      *gorm.DB
	generic *store.GenericStore[*model.EnrollmentApprovalPolicy, model.EnrollmentApprovalPolicy, domain.EnrollmentApprovalPolicy, domain.EnrollmentApprovalPolicyList]
}

func NewStore(db *gorm.DB, log logrus.FieldLogger) Store {
	if db == nil || log == nil {
		panic("policy database and logger are required")
	}
	return &PolicyStore{db: db, generic: store.NewGenericStore[*model.EnrollmentApprovalPolicy, model.EnrollmentApprovalPolicy, domain.EnrollmentApprovalPolicy, domain.EnrollmentApprovalPolicyList](db, log, model.NewEnrollmentApprovalPolicyFromApiResource, (*model.EnrollmentApprovalPolicy).ToApiResource, model.EnrollmentApprovalPoliciesToApiResource)}
}

func (policies *PolicyStore) InitialMigration(ctx context.Context) error {
	db := store.DB(ctx, policies.db)
	if err := db.AutoMigrate(&model.EnrollmentApprovalPolicy{}); err != nil {
		return err
	}
	return db.Exec("CREATE INDEX IF NOT EXISTS idx_approval_policy_org_credential ON enrollment_approval_policies (org_id, certificate_name)").Error
}

func (policies *PolicyStore) Get(ctx context.Context, orgID uuid.UUID, name string) (*domain.EnrollmentApprovalPolicy, error) {
	return policies.generic.Get(ctx, orgID, name)
}

func (policies *PolicyStore) List(ctx context.Context, orgID uuid.UUID, params store.ListParams) (*domain.EnrollmentApprovalPolicyList, error) {
	return policies.generic.List(ctx, orgID, params)
}

// policySetLock restricts writes to the credential and policy locked by the transaction.
type policySetLock struct {
	orgID                 uuid.UUID
	certificateName, name string
}
type policySetLockKey struct{}

func (policies *PolicyStore) WithPolicySetTransaction(ctx context.Context, orgID uuid.UUID, certificateName, name string, fn func(context.Context, *domain.CertificateSigningRequest, []domain.EnrollmentApprovalPolicy) error) error {
	if !store.InTransaction(ctx) {
		return store.WithTransaction(ctx, policies.db, func(txCtx context.Context) error {
			return policies.WithPolicySetTransaction(txCtx, orgID, certificateName, name, fn)
		})
	}
	db := store.DB(ctx, policies.db)
	var credential model.CertificateSigningRequest
	err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND name = ? AND spec IS NOT NULL", orgID, certificateName).First(&credential).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fn(ctx, nil, nil)
	}
	if err != nil {
		return persistenceError(err)
	}
	resource, err := credential.ToApiResource()
	if err != nil {
		return err
	}
	var records []model.EnrollmentApprovalPolicy
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND certificate_name = ? AND name <> ? AND spec ->> 'enabled' = 'true'", orgID, certificateName, name).Order("name").Find(&records).Error; err != nil {
		return persistenceError(err)
	}
	enabled := make([]domain.EnrollmentApprovalPolicy, 0, len(records))
	for _, record := range records {
		policy, err := record.ToApiResource()
		if err != nil {
			return err
		}
		enabled = append(enabled, *policy)
	}
	lockedCtx := context.WithValue(ctx, policySetLockKey{}, policySetLock{orgID, certificateName, name})
	return fn(lockedCtx, resource, enabled)
}

func lockedPolicySet(ctx context.Context, orgID uuid.UUID, name string) (policySetLock, error) {
	lock, ok := ctx.Value(policySetLockKey{}).(policySetLock)
	if !ok || !store.InTransaction(ctx) {
		return lock, fmt.Errorf("policy write requires a locked policy set transaction")
	}
	if lock.orgID != orgID || lock.name != name {
		return lock, fmt.Errorf("policy write does not match the locked policy set")
	}
	return lock, nil
}

func (policies *PolicyStore) advanceRevision(ctx context.Context, lock policySetLock) error {
	return store.DB(ctx, policies.db).Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", lock.orgID, lock.certificateName).UpdateColumn("policy_set_revision", gorm.Expr("COALESCE(policy_set_revision, 0) + 1")).Error
}

func (policies *PolicyStore) Mutate(ctx context.Context, orgID uuid.UUID, name string, apply ApplyFunc) (*domain.EnrollmentApprovalPolicy, *domain.EnrollmentApprovalPolicy, bool, error) {
	lock, err := lockedPolicySet(ctx, orgID, name)
	if err != nil {
		return nil, nil, false, err
	}
	hooks := store.MutateHooks[domain.EnrollmentApprovalPolicy]{
		MaxRetries: lo.ToPtr(0),
		Load:       policies.load,
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
	updated, before, created, err := policies.generic.Mutate(ctx, orgID, name, nil, hooks, func(m store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
		if apply == nil {
			return fmt.Errorf("policy mutation requires apply")
		}
		if err := apply(m); err != nil {
			return err
		}
		if m.Resource() == nil || lo.FromPtr(m.Resource().Metadata.Name) != name || m.Resource().Spec.CertificateName != lock.certificateName {
			return flterrors.ErrImmutableCertificateBinding
		}
		return nil
	})
	if err == nil {
		err = policies.advanceRevision(ctx, lock)
	}
	if err != nil {
		return nil, before, false, err
	}
	return updated, before, created, nil
}

func (policies *PolicyStore) load(ctx context.Context, orgID uuid.UUID, name string) (*domain.EnrollmentApprovalPolicy, error) {
	var record model.EnrollmentApprovalPolicy
	err := store.DB(ctx, policies.db).Unscoped().Where("org_id = ? AND name = ?", orgID, name).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, persistenceError(err)
	}
	if record.DeletedAt.Valid {
		return nil, ErrNameReserved
	}
	return record.ToApiResource()
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
	record.Generation = before.Metadata.Generation
	if !record.HasSameSpecAs(previous) {
		record.Generation = lo.ToPtr(lo.FromPtr(before.Metadata.Generation) + 1)
	}
	// RETURNING populates server-owned status without including it in the update.
	result := store.DB(ctx, policies.db).Model(record).Where("org_id = ? AND name = ? AND resource_version = ?", orgID, name, previous.ResourceVersion).Clauses(clause.Returning{}).Select("spec", "labels", "annotations", "resource_version", "generation", "updated_at").Updates(record)
	if result.Error != nil {
		return false, persistenceError(result.Error)
	}
	if result.RowsAffected != 1 {
		return false, flterrors.ErrResourceVersionConflict
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
	lock, err := lockedPolicySet(ctx, orgID, name)
	if err != nil {
		return err
	}
	result := store.DB(ctx, policies.db).Where("org_id = ? AND name = ? AND certificate_name = ?", orgID, name, lock.certificateName).Delete(&model.EnrollmentApprovalPolicy{})
	if result.Error != nil {
		return persistenceError(result.Error)
	}
	if result.RowsAffected != 1 {
		return flterrors.ErrResourceNotFound
	}
	return policies.advanceRevision(ctx, lock)
}

func persistenceError(err error) error {
	mapped := store.ErrorFromGormError(err)
	if mapped == err {
		return err
	}
	return errors.Join(mapped, err)
}
