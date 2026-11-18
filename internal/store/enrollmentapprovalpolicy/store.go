package enrollmentapprovalpolicy

import (
	"context"
	"fmt"
	"strconv"

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

type Operation int

const (
	Create Operation = iota
	Update
	Delete
)

type ApplyFunc func(*domain.EnrollmentApprovalPolicy, *domain.CertificateSigningRequest, []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error)

type Store interface {
	InitialMigration(context.Context) error
	Get(context.Context, uuid.UUID, string) (*domain.EnrollmentApprovalPolicy, error)
	List(context.Context, uuid.UUID, store.ListParams) (*domain.EnrollmentApprovalPolicyList, error)
	Mutate(context.Context, uuid.UUID, string, string, Operation, ApplyFunc) (*domain.EnrollmentApprovalPolicy, *domain.EnrollmentApprovalPolicy, error)
}

type PolicyStore struct {
	db      *gorm.DB
	generic *store.GenericStore[*model.EnrollmentApprovalPolicy, model.EnrollmentApprovalPolicy, domain.EnrollmentApprovalPolicy, domain.EnrollmentApprovalPolicyList]
}

func NewStore(db *gorm.DB, log logrus.FieldLogger) Store {
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

func (policies *PolicyStore) Mutate(ctx context.Context, orgID uuid.UUID, name, certificateName string, operation Operation, apply ApplyFunc) (*domain.EnrollmentApprovalPolicy, *domain.EnrollmentApprovalPolicy, error) {
	if apply == nil {
		return nil, nil, fmt.Errorf("policy mutation requires validation")
	}
	var result, previous *domain.EnrollmentApprovalPolicy
	err := store.RunInTransaction(ctx, policies.db, func(tx *gorm.DB) error {
		var credential model.CertificateSigningRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND name = ? AND spec IS NOT NULL", orgID, certificateName).First(&credential).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		credentialResource, err := credential.ToApiResource()
		if err != nil {
			return err
		}
		var existing model.EnrollmentApprovalPolicy
		query := tx.Unscoped().Where("org_id = ? AND name = ?", orgID, name).Take(&existing)
		var current *domain.EnrollmentApprovalPolicy
		if query.Error != nil && query.Error != gorm.ErrRecordNotFound {
			return store.ErrorFromGormError(query.Error)
		}
		if operation == Create {
			if query.Error == nil {
				return flterrors.ErrDuplicateName
			}
		} else {
			if query.Error == gorm.ErrRecordNotFound || existing.DeletedAt.Valid {
				return flterrors.ErrResourceNotFound
			}
			if existing.CertificateName != certificateName {
				return flterrors.ErrImmutableCertificateBinding
			}
			previous, err = existing.ToApiResource()
			if err != nil {
				return err
			}
			current, err = store.CloneJSON(previous)
			if err != nil {
				return err
			}
		}
		var records []model.EnrollmentApprovalPolicy
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND certificate_name = ? AND name <> ? AND spec ->> 'enabled' = 'true'", orgID, certificateName, name).Order("name").Find(&records).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		enabled := make([]domain.EnrollmentApprovalPolicy, 0, len(records))
		for _, record := range records {
			resource, err := record.ToApiResource()
			if err != nil {
				return err
			}
			enabled = append(enabled, *resource)
		}
		candidate, err := apply(current, credentialResource, enabled)
		if err != nil {
			return err
		}
		if operation == Delete {
			if err := tx.Delete(&existing).Error; err != nil {
				return store.ErrorFromGormError(err)
			}
		} else {
			if candidate == nil || candidate.Metadata.Name == nil || *candidate.Metadata.Name != name || candidate.Spec.CertificateName != certificateName {
				return flterrors.ErrImmutableCertificateBinding
			}
			result, err = persistPolicy(tx, orgID, &existing, candidate, operation)
			if err != nil {
				return err
			}
		}
		return tx.Model(&credential).UpdateColumn("policy_set_revision", gorm.Expr("COALESCE(policy_set_revision, 0) + 1")).Error
	})
	if err != nil {
		return nil, previous, err
	}
	return result, previous, nil
}

func persistPolicy(tx *gorm.DB, orgID uuid.UUID, existing *model.EnrollmentApprovalPolicy, candidate *domain.EnrollmentApprovalPolicy, operation Operation) (*domain.EnrollmentApprovalPolicy, error) {
	record, err := model.NewEnrollmentApprovalPolicyFromApiResource(candidate)
	if err != nil {
		return nil, err
	}
	record.OrgID = orgID
	if operation == Create {
		record.Status = model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{})
		record.ResourceVersion = lo.ToPtr(int64(1))
		record.Generation = lo.ToPtr(int64(1))
		if err := tx.Clauses(clause.Returning{}).Create(record).Error; err != nil {
			return nil, store.ErrorFromGormError(err)
		}
	} else {
		if candidate.Metadata.ResourceVersion == nil || *candidate.Metadata.ResourceVersion != strconv.FormatInt(lo.FromPtr(existing.ResourceVersion), 10) {
			return nil, flterrors.ErrResourceVersionConflict
		}
		record.Status = existing.Status
		record.ResourceVersion = lo.ToPtr(lo.FromPtr(existing.ResourceVersion) + 1)
		record.Generation = existing.Generation
		if !record.HasSameSpecAs(existing) {
			record.Generation = lo.ToPtr(lo.FromPtr(existing.Generation) + 1)
		}
		record.CreatedAt = existing.CreatedAt
		updated := tx.Model(existing).Where("resource_version = ?", existing.ResourceVersion).Clauses(clause.Returning{}).Select("spec", "labels", "annotations", "resource_version", "generation", "updated_at").Updates(record)
		if updated.Error != nil {
			return nil, store.ErrorFromGormError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return nil, flterrors.ErrResourceVersionConflict
		}
		record = existing
	}
	return record.ToApiResource()
}
