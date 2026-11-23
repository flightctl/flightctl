package enrollmentapprovalpolicy

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
)

func (policies *PolicyStore) InitialMigration(ctx context.Context) error {
	db := store.DB(ctx, policies.db)
	policy := &model.EnrollmentApprovalPolicy{}
	if err := db.AutoMigrate(policy); err != nil {
		return fmt.Errorf("create enrollment approval policy table: %w", err)
	}
	if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_approval_policy_org_credential ON enrollment_approval_policies (org_id, certificate_name)").Error; err != nil {
		return fmt.Errorf("create enrollment approval policy credential index: %w", err)
	}

	if !db.Migrator().HasConstraint(policy, store.EnrollmentPolicyCredentialBindingCheck) {
		if err := db.Exec(`ALTER TABLE enrollment_approval_policies
			ADD CONSTRAINT ` + store.EnrollmentPolicyCredentialBindingCheck + `
			CHECK ((spec->'certificateName') IS NOT DISTINCT FROM to_jsonb(certificate_name))`).Error; err != nil {
			return fmt.Errorf("validate stored policy certificate names: %w", err)
		}
	}
	if !db.Migrator().HasConstraint(policy, store.EnrollmentPolicyCredentialFK) {
		if err := db.Exec(`ALTER TABLE enrollment_approval_policies
			ADD CONSTRAINT ` + store.EnrollmentPolicyCredentialFK + `
			FOREIGN KEY (org_id, certificate_name)
			REFERENCES certificate_signing_requests (org_id, name) ON DELETE RESTRICT`).Error; err != nil {
			return fmt.Errorf("validate policy references to enrollment credentials: %w", err)
		}
	}

	return nil
}
