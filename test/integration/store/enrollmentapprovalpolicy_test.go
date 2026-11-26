package store_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/migration"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("EnrollmentApprovalPolicyStore", func() {
	var ctx context.Context
	var cancel context.CancelFunc
	var workers []<-chan struct{}
	var db *gorm.DB
	var cfg *config.Config
	var dbName string
	var orgID uuid.UUID
	var log *logrus.Logger
	var policies policystore.Store
	BeforeEach(func() {
		workers = nil
		ctx, cancel = context.WithTimeout(testutil.StartSpecTracerForGinkgo(suiteCtx), 15*time.Second)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		orgID = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), orgID)).To(Succeed())
		policies, err = policystore.NewStore(db, log)
		Expect(err).NotTo(HaveOccurred())
		Expect(db.WithContext(ctx).Create(&model.CertificateSigningRequest{Resource: model.Resource{OrgID: orgID, Name: "credential"}, Spec: model.MakeJSONField(domain.CertificateSigningRequestSpec{SignerName: "enrollment"})}).Error).To(Succeed())
	})
	AfterEach(func() {
		cancel()
		for _, done := range workers {
			Eventually(done, 5*time.Second).Should(BeClosed(), "policy worker must finish before database cleanup")
		}
		Expect(testdb.DeleteTestDB(suiteCtx, log, cfg, db, dbName)).To(Succeed())
	})
	launch := func(fn func()) {
		done := make(chan struct{})
		workers = append(workers, done)
		go func() { defer close(done); fn() }()
	}
	create := func(name string, fail error) (*domain.EnrollmentApprovalPolicy, error) {
		result, _, err := mutatePolicy(ctx, policies, orgID, name, func(_ *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			return &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}, Spec: domain.EnrollmentApprovalPolicySpec{CertificateName: "credential", MatchExpression: "true", DeviceLabels: map[string]string{}}}, fail
		})
		return result, err
	}
	DescribeTable("When migrating pre-constraint policies it should preserve stored bindings and accounting", func(orphan bool) {
		migrationDB, err := store.InitMigrationDB(cfg, log)
		Expect(err).NotTo(HaveOccurred())
		defer testdb.CloseDB(migrationDB)
		ddl := migrationDB.WithContext(store.WithBypassSpanCheck(ctx))
		hasFK := func() bool {
			return ddl.Migrator().HasConstraint(&model.EnrollmentApprovalPolicy{}, store.EnrollmentPolicyCredentialFK)
		}
		Expect(hasFK()).To(BeTrue(), "fresh migration should install the credential FK")
		Expect(ddl.Exec("ALTER TABLE enrollment_approval_policies DROP CONSTRAINT " + store.EnrollmentPolicyCredentialFK).Error).To(Succeed())
		Expect(ddl.Exec("ALTER TABLE enrollment_approval_policies DROP CONSTRAINT " + store.EnrollmentPolicyCredentialBindingCheck + ", ALTER COLUMN certificate_name DROP NOT NULL").Error).To(Succeed())

		for _, name := range []string{"live", "retained"} {
			binding := "credential"
			if orphan && name == "retained" {
				binding = "absent"
			}
			policy := model.EnrollmentApprovalPolicy{
				Resource:        model.Resource{OrgID: orgID, Name: name},
				CertificateName: binding,
				Spec:            model.MakeJSONField(domain.EnrollmentApprovalPolicySpec{CertificateName: binding}),
				Status:          model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: math.MaxInt64}),
			}
			if name == "retained" {
				policy.DeletedAt = gorm.DeletedAt{Time: time.Now().UTC(), Valid: true}
			}
			Expect(db.WithContext(ctx).Create(&policy).Error).To(Succeed())
		}
		readRows := func() []model.EnrollmentApprovalPolicy {
			var rows []model.EnrollmentApprovalPolicy
			Expect(db.WithContext(ctx).Unscoped().Where("org_id = ?", orgID).Order("name").Find(&rows).Error).To(Succeed())
			return rows
		}
		before := readRows()
		dryRun := migration.Run(ctx, migrationDB, log, true, cfg.CA.DeviceEnrollmentSignerName)
		Expect(hasFK()).To(BeFalse())
		Expect(readRows()).To(Equal(before))
		err = migration.Run(ctx, migrationDB, log, false, cfg.CA.DeviceEnrollmentSignerName)
		if orphan {
			Expect(store.IsEnrollmentPolicyCredentialFKViolation(dryRun)).To(BeTrue(), "%v", dryRun)
			Expect(store.IsEnrollmentPolicyCredentialFKViolation(err)).To(BeTrue(), "%v", err)
			Expect(hasFK()).To(BeFalse())
		} else {
			Expect(dryRun).To(MatchError(migration.ErrDryRunComplete))
			Expect(err).NotTo(HaveOccurred())
			Expect(hasFK()).To(BeTrue())
			Expect(migration.Run(ctx, migrationDB, log, false, cfg.CA.DeviceEnrollmentSignerName)).To(Succeed())
			var mandatory bool
			Expect(db.WithContext(ctx).Raw(`SELECT convalidated AND confdeltype = 'r' AND attnotnull
				FROM pg_constraint JOIN pg_attribute ON attrelid = conrelid AND attname = 'certificate_name'
				WHERE conrelid = 'public.enrollment_approval_policies'::regclass AND conname = ?`, store.EnrollmentPolicyCredentialFK).Scan(&mandatory).Error).To(Succeed())
			Expect(mandatory).To(BeTrue())
			csrs := csrstore.NewCertificateSigningRequestStore(db, log, "enrollment")
			_, err = csrs.Delete(ctx, orgID, "credential")
			Expect(store.IsEnrollmentPolicyCredentialFKViolation(err)).To(BeTrue(), "%v", err)
		}
		Expect(readRows()).To(Equal(before))
	},
		Entry("When existing live and retained rows are valid it should migrate idempotently", false),
		Entry("When retained history is orphaned it should reject and roll back the migration", true),
	)

	It("When tenants share a credential name it should enforce the qualified reference and immutable binding", func() {
		other := uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), other)).To(Succeed())
		row := model.EnrollmentApprovalPolicy{
			Resource:        model.Resource{OrgID: other, Name: "west"},
			CertificateName: "credential",
			Spec:            model.MakeJSONField(domain.EnrollmentApprovalPolicySpec{CertificateName: "credential"}),
		}
		err := db.WithContext(ctx).Create(&row).Error
		Expect(store.IsEnrollmentPolicyCredentialFKViolation(err)).To(BeTrue())
		var violation *pgconn.PgError
		Expect(errors.As(err, &violation)).To(BeTrue())
		Expect(violation.ConstraintName).To(Equal(store.EnrollmentPolicyCredentialFK))

		_, err = create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		policy := func() *gorm.DB {
			return db.WithContext(ctx).Model(&model.EnrollmentApprovalPolicy{}).Where("org_id = ? AND name = ?", orgID, "west")
		}
		expectImmutableBinding := func(err error) {
			Expect(errors.Is(err, flterrors.ErrImmutableCertificateBinding)).To(BeTrue(), "%v", err)
			Expect(errors.As(err, &violation)).To(BeTrue())
			Expect(violation.Code).To(Equal("23514"))
			Expect(violation.ConstraintName).To(Equal(store.EnrollmentPolicyCredentialBindingCheck))
		}
		expectImmutableBinding(policy().UpdateColumn("spec", gorm.Expr("jsonb_set(spec, '{certificateName}', to_jsonb(?::text))", "other")).Error)
		saved, err := policies.Get(ctx, orgID, "west")
		Expect(err).NotTo(HaveOccurred())
		Expect(saved.Spec.CertificateName).To(Equal("credential"))

		Expect(db.WithContext(ctx).Create(&model.CertificateSigningRequest{
			Resource: model.Resource{OrgID: other, Name: "42"},
			Spec:     model.MakeJSONField(domain.CertificateSigningRequestSpec{SignerName: "enrollment"}),
		}).Error).To(Succeed())
		expectImmutableBinding(db.WithContext(ctx).Exec(`INSERT INTO enrollment_approval_policies (org_id, name, certificate_name, spec)
			VALUES (?, 'numeric', '42', '{"certificateName":42}')`, other).Error)
	})

	It("When a direct caller skips a write it should still check the persisted binding", func() {
		created, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		for _, changed := range []bool{false, true} {
			updated, before, creating, err := policies.Mutate(ctx, orgID, "west", func(m store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
				if changed {
					m.Resource().Spec.CertificateName = "other"
				}
				return store.ErrMutateSkipWrite
			})
			Expect(creating).To(BeFalse())
			if changed {
				Expect(err).To(MatchError(flterrors.ErrImmutableCertificateBinding))
			} else {
				Expect(err).NotTo(HaveOccurred())
				Expect(updated).To(Equal(before))
			}
		}
		// Verify the stored column even for legacy rows whose JSON has drifted.
		migrationDB, err := store.InitMigrationDB(cfg, log)
		Expect(err).NotTo(HaveOccurred())
		defer testdb.CloseDB(migrationDB)
		ddl := migrationDB.WithContext(store.WithBypassSpanCheck(ctx))
		Expect(ddl.Exec("ALTER TABLE enrollment_approval_policies DROP CONSTRAINT " + store.EnrollmentPolicyCredentialBindingCheck).Error).To(Succeed())
		Expect(db.WithContext(ctx).Exec(`UPDATE enrollment_approval_policies
			SET spec = jsonb_set(spec, '{certificateName}', '"other"') WHERE org_id = ? AND name = 'west'`, orgID).Error).To(Succeed())
		_, _, _, err = policies.Mutate(ctx, orgID, "west", func(store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
			return store.ErrMutateSkipWrite
		})
		Expect(err).To(MatchError(flterrors.ErrImmutableCertificateBinding))
		Expect(created.Spec.CertificateName).To(Equal("credential"))
	})

	It("When validation fails it should leave no policy writes", func() {
		unavailable := errors.New("validation unavailable")
		_, err := create("west", unavailable)
		Expect(err).To(MatchError(unavailable))
		_, err = policies.Get(ctx, orgID, "west")
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
	})
	It("When policy is deleted it should retain count and reserve its name", func() {
		created, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.Status.EnrollmentCount).To(Equal(int64(0)))
		Expect(db.WithContext(ctx).Model(&model.EnrollmentApprovalPolicy{}).Where("org_id = ? AND name = ?", orgID, "west").Update("status", model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: math.MaxInt64})).Error).To(Succeed())
		_, err = policies.Get(ctx, uuid.New(), "west")
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		err = deletePolicy(ctx, policies, orgID, "west")
		Expect(err).NotTo(HaveOccurred())
		_, err = policies.Get(ctx, orgID, "west")
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		var retained model.EnrollmentApprovalPolicy
		Expect(db.WithContext(ctx).Unscoped().Where("org_id = ? AND name = ?", orgID, "west").Take(&retained).Error).To(Succeed())
		Expect(retained.Status.Data.EnrollmentCount).To(Equal(int64(math.MaxInt64)))
		_, err = create("west", nil)
		Expect(err).To(MatchError(flterrors.ErrDuplicateName))
	})
	It("When updating policy configuration it should preserve count and reject stale versions", func() {
		created, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(db.WithContext(ctx).Model(&model.EnrollmentApprovalPolicy{}).Where("org_id = ? AND name = ?", orgID, "west").Update("status", model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: math.MaxInt32 + 1})).Error).To(Succeed())
		updated, _, err := mutatePolicy(ctx, policies, orgID, "west", func(current *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			current.Spec.Enabled = true
			current.Status = &domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: 0}
			return current, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Status.EnrollmentCount).To(Equal(int64(math.MaxInt32 + 1)))
		Expect(*updated.Metadata.ResourceVersion).To(Equal("2"))
		_, _, err = mutatePolicy(ctx, policies, orgID, "west", func(_ *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			return created, nil
		})
		Expect(err).To(MatchError(flterrors.ErrResourceVersionConflict))
	})
	It("When a concurrent update wins the compare-and-swap it should reapply to the latest version", func() {
		_, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())

		var versions []string
		updated, before, created, err := policies.Mutate(ctx, orgID, "west", func(m store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
			current := m.Resource()
			versions = append(versions, *current.Metadata.ResourceVersion)
			if len(versions) == 1 {
				_, _, err := mutatePolicy(ctx, policies, orgID, "west", func(other *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
					other.Spec.DeviceLabels["region"] = "west"
					return other, nil
				})
				Expect(err).NotTo(HaveOccurred())
			}
			current.Spec.DeviceLabels["stage"] = "ready"
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(versions).To(Equal([]string{"1", "2"}))
		Expect(before.Spec.DeviceLabels).To(Equal(map[string]string{"region": "west"}))
		Expect(*updated.Metadata.ResourceVersion).To(Equal("3"))
		Expect(updated.Spec.DeviceLabels).To(Equal(map[string]string{"region": "west", "stage": "ready"}))
	})
	It("When a mutation changes nested spec maps it should retain the before snapshot and advance generation", func() {
		_, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		updated, previous, err := mutatePolicy(ctx, policies, orgID, "west", func(current *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			current.Spec.DeviceLabels["region"] = "west"
			return current, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(previous.Spec.DeviceLabels).To(BeEmpty())
		Expect(updated.Spec.DeviceLabels).To(HaveKeyWithValue("region", "west"))
		Expect(*updated.Metadata.Generation).To(Equal(int64(2)))
	})
	It("When the configured signer changes it should retain FK deletion protection for live and deleted bindings", func() {
		_, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		credentials := csrstore.NewCertificateSigningRequestStore(db, log, "changed-signer")
		for _, deletedPolicy := range []bool{false, true} {
			if deletedPolicy {
				err = deletePolicy(ctx, policies, orgID, "west")
				Expect(err).NotTo(HaveOccurred())
			}
			_, err = credentials.Delete(ctx, orgID, "credential")
			Expect(store.IsEnrollmentPolicyCredentialFKViolation(err)).To(BeTrue(), "%v", err)
			credential, err := credentials.Get(ctx, orgID, "credential")
			Expect(err).NotTo(HaveOccurred())
			credential.Metadata.Labels = lo.ToPtr(map[string]string{"retained": "true"})
			updated, _, err := credentials.Update(ctx, orgID, credential)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.Metadata.Labels).To(Equal(credential.Metadata.Labels))
			updated.Metadata.Labels = lo.ToPtr(map[string]string{"retained": "edited"})
			updated, _, created, err := credentials.CreateOrUpdate(ctx, orgID, updated)
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(*updated.Metadata.Labels).To(HaveKeyWithValue("retained", "edited"))
		}
	})
	It("When same-version policy updates race it should commit exactly one", func() {
		created, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		results := make(chan error, 2)
		start := make(chan struct{})
		for range 2 {
			launch(func() {
				select {
				case <-start:
				case <-ctx.Done():
					return
				}
				_, _, err := mutatePolicy(ctx, policies, orgID, "west", func(current *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
					current.Metadata.ResourceVersion = created.Metadata.ResourceVersion
					current.Spec.Enabled = true
					return current, nil
				})
				results <- err
			})
		}
		close(start)
		first, second := receivePolicyResult(ctx, results), receivePolicyResult(ctx, results)
		if first == nil {
			Expect(second).To(MatchError(flterrors.ErrResourceVersionConflict))
		} else {
			Expect(first).To(MatchError(flterrors.ErrResourceVersionConflict))
			Expect(second).NotTo(HaveOccurred())
		}
	})
	It("When a bound credential is revoked it should retain policy accounting and deletion protection", func() {
		_, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		credentials := csrstore.NewCertificateSigningRequestStore(db, log, "enrollment")
		updated, before, created, err := credentials.Mutate(ctx, orgID, "credential", nil, func(mutation *csrstore.CertificateSigningRequestMutation) error {
			return mutation.ApplyRevocation(domain.EnrollmentCertificateRevocation{Reason: "credential retired", RevokedAt: time.Now().UTC(), RevokedBy: "administrator"})
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(before.Status.HasRevocation()).To(BeFalse())
		Expect(updated.Status.HasRevocation()).To(BeTrue())
		policy, err := policies.Get(ctx, orgID, "west")
		Expect(err).NotTo(HaveOccurred())
		Expect(policy.Spec.CertificateName).To(Equal("credential"))
		Expect(policy.Status.EnrollmentCount).To(Equal(int64(0)))
		_, err = credentials.Delete(ctx, orgID, "credential")
		Expect(err).To(MatchError(flterrors.ErrRevocationConsequencesPending))
		_, _, _, err = credentials.Mutate(ctx, orgID, "credential", nil, func(mutation *csrstore.CertificateSigningRequestMutation) error {
			mutation.Resource().Status.Revocation.FlaggingCompletedAt = lo.ToPtr(time.Now().UTC())
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = credentials.Delete(ctx, orgID, "credential")
		Expect(store.IsEnrollmentPolicyCredentialFKViolation(err)).To(BeTrue(), "%v", err)
	})
	DescribeTable("When binding creation and hard deletion contend it should prevent an orphan", func(insertFirst bool) {
		csrs := csrstore.NewCertificateSigningRequestStore(db, log, "enrollment")
		ready := make(chan struct{}, 1)
		release := make(chan struct{})
		defer closeIfOpen(release)
		firstResult, secondResult := make(chan error, 1), make(chan error, 1)
		secondPID := make(chan int, 1)
		insert := func(txCtx context.Context) error {
			_, _, err := mutatePolicy(txCtx, policies, orgID, "west", func(_ *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
				return &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr("west")}, Spec: domain.EnrollmentApprovalPolicySpec{CertificateName: "credential"}}, nil
			})
			return err
		}
		remove := func(txCtx context.Context) error {
			deleted, err := csrs.Delete(txCtx, orgID, "credential")
			if err == nil && !deleted {
				return errors.New("expected an existing CSR to be deleted")
			}
			return err
		}
		first, second := insert, remove
		if !insertFirst {
			first, second = remove, insert
		}
		launch(func() {
			firstResult <- store.WithTransaction(ctx, db, func(txCtx context.Context) error {
				if err := first(txCtx); err != nil {
					return err
				}
				ready <- struct{}{}
				select {
				case <-release:
					return nil
				case <-txCtx.Done():
					return txCtx.Err()
				}
			})
		})
		Eventually(ready, 5*time.Second).Should(Receive())
		launch(func() {
			secondResult <- store.WithTransaction(ctx, db, func(txCtx context.Context) error {
				var pid int
				if err := store.DB(txCtx, db).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
					return err
				}
				secondPID <- pid
				return second(txCtx)
			})
		})
		var pid int
		Eventually(secondPID, 5*time.Second).Should(Receive(&pid))
		expectBlockedPolicyTransaction(ctx, db, pid)
		close(release)
		Expect(receivePolicyResult(ctx, firstResult)).To(Succeed())
		err := receivePolicyResult(ctx, secondResult)
		_, readErr := policies.Get(ctx, orgID, "west")
		_, csrErr := csrs.Get(ctx, orgID, "credential")
		if insertFirst {
			Expect(store.IsEnrollmentPolicyCredentialFKViolation(err)).To(BeTrue(), "%v", err)
			Expect(readErr).NotTo(HaveOccurred())
			Expect(csrErr).NotTo(HaveOccurred())
		} else {
			Expect(store.IsEnrollmentPolicyCredentialFKViolation(err)).To(BeTrue(), "%v", err)
			Expect(readErr).To(MatchError(flterrors.ErrResourceNotFound))
			Expect(csrErr).To(MatchError(flterrors.ErrResourceNotFound))
		}
	},
		Entry("When policy insertion wins it should reject deletion with the named foreign key", true),
		Entry("When deletion wins it should reject insertion with the named foreign key", false),
	)
	It("When caller transactions roll back it should undo policy writes", func() {
		rollback := errors.New("rollback")
		err := store.WithTransaction(ctx, db, func(txCtx context.Context) error {
			_, _, err := mutatePolicy(txCtx, policies, orgID, "west", func(_ *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
				return &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr("west")}, Spec: domain.EnrollmentApprovalPolicySpec{CertificateName: "credential", MatchExpression: "true", DeviceLabels: map[string]string{}}, Status: &domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: math.MaxInt64}}, nil
			})
			if err != nil {
				return err
			}
			return rollback
		})
		Expect(err).To(MatchError(rollback))
		_, err = policies.Get(ctx, orgID, "west")
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		original, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		for _, deleting := range []bool{false, true} {
			err = store.WithTransaction(ctx, db, func(txCtx context.Context) error {
				var err error
				if deleting {
					err = deletePolicy(txCtx, policies, orgID, "west")
				} else {
					_, _, err = mutatePolicy(txCtx, policies, orgID, "west", func(current *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
						current.Spec.Enabled = true
						current.Status.EnrollmentCount = math.MaxInt64
						return current, nil
					})
				}
				if err != nil {
					return err
				}
				return rollback
			})
			Expect(err).To(MatchError(rollback))
			current, err := policies.Get(ctx, orgID, "west")
			Expect(err).NotTo(HaveOccurred())
			Expect(current).To(Equal(original))
		}
	})
	It("When a policy is created with forged accounting it should initialize zero", func() {
		result, _, err := mutatePolicy(ctx, policies, orgID, "west", func(_ *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			return &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr("west")}, Spec: domain.EnrollmentApprovalPolicySpec{CertificateName: "credential", MatchExpression: "true"}, Status: &domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: math.MaxInt64}}, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status.EnrollmentCount).To(BeZero())
	})

	It("When organizations share names it should isolate zero-organization mutations", func() {
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), uuid.Nil)).To(Succeed())
		Expect(db.WithContext(ctx).Create(&model.CertificateSigningRequest{Resource: model.Resource{OrgID: uuid.Nil, Name: "credential"}, Spec: model.MakeJSONField(domain.CertificateSigningRequestSpec{SignerName: "enrollment"})}).Error).To(Succeed())
		foreign, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		zero, _, err := mutatePolicy(ctx, policies, uuid.Nil, "west", func(_ *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			return &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr("west")}, Spec: domain.EnrollmentApprovalPolicySpec{CertificateName: "credential", MatchExpression: "true"}}, nil
		})
		Expect(err).NotTo(HaveOccurred())
		updated, _, err := mutatePolicy(ctx, policies, uuid.Nil, "west", func(current *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			current.Spec.Enabled = true
			return current, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Metadata.ResourceVersion).NotTo(Equal(zero.Metadata.ResourceVersion))
		Expect(deletePolicy(ctx, policies, uuid.Nil, "west")).To(Succeed())
		preserved, err := policies.Get(ctx, orgID, "west")
		Expect(err).NotTo(HaveOccurred())
		Expect(preserved).To(Equal(foreign))
	})

})

func mutatePolicy(ctx context.Context, policies policystore.Store, orgID uuid.UUID, name string, prepare func(*domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error)) (result, previous *domain.EnrollmentApprovalPolicy, err error) {
	err = policies.WithTransaction(ctx, func(txCtx context.Context) error {
		var err error
		result, previous, _, err = policies.Mutate(txCtx, orgID, name, func(m store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
			candidate, err := prepare(m.Resource())
			if err == nil {
				m.SetResource(candidate)
			}
			return err
		})
		return err
	})
	return
}

func deletePolicy(ctx context.Context, policies policystore.Store, orgID uuid.UUID, name string) error {
	return policies.Delete(ctx, orgID, name)
}

func receivePolicyResult(ctx context.Context, results <-chan error) error {
	select {
	case result := <-results:
		return result
	case <-ctx.Done():
		return fmt.Errorf("waiting for policy mutation: %w", ctx.Err())
	}
}

func expectBlockedPolicyTransaction(ctx context.Context, db *gorm.DB, pid int) {
	Eventually(func(g Gomega) {
		var blocked bool
		g.Expect(db.WithContext(ctx).Raw("SELECT cardinality(pg_blocking_pids(?)) > 0", pid).Scan(&blocked).Error).To(Succeed())
		g.Expect(blocked).To(BeTrue())
	}, 5*time.Second).Should(Succeed())
}

func closeIfOpen(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}
