package store_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("EnrollmentApprovalPolicyStore", func() {
	var ctx context.Context
	var db *gorm.DB
	var cfg *config.Config
	var dbName string
	var orgID uuid.UUID
	var log *logrus.Logger
	var policies policystore.Store
	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		orgID = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), orgID)).To(Succeed())
		policies = policystore.NewStore(db, log)
		Expect(db.Create(&model.CertificateSigningRequest{Resource: model.Resource{OrgID: orgID, Name: "credential"}, Spec: model.MakeJSONField(domain.CertificateSigningRequestSpec{SignerName: "enrollment"})}).Error).To(Succeed())
	})
	AfterEach(func() { Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed()) })
	create := func(name string, fail error) (*domain.EnrollmentApprovalPolicy, error) {
		result, _, err := policies.Mutate(ctx, orgID, name, "credential", policystore.Create, func(_ *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			return &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}, Spec: domain.EnrollmentApprovalPolicySpec{CertificateName: "credential", MatchExpression: "true", DeviceLabels: map[string]string{}}}, fail
		})
		return result, err
	}
	It("When migrating an existing credential it should initialize and advance the policy revision", func() {
		migrationDB, err := store.InitMigrationDB(cfg, log)
		Expect(err).NotTo(HaveOccurred())
		defer testdb.CloseDB(migrationDB)
		Expect(migrationDB.WithContext(ctx).Migrator().DropColumn(&model.CertificateSigningRequest{}, "PolicySetRevision")).To(Succeed())
		credentials := csrstore.NewCertificateSigningRequestStore(migrationDB, log, "enrollment")
		Expect(credentials.InitialMigration(ctx)).To(Succeed())
		var isNull bool
		Expect(db.Raw("SELECT policy_set_revision IS NULL FROM certificate_signing_requests WHERE org_id = ? AND name = ?", orgID, "credential").Scan(&isNull).Error).To(Succeed())
		Expect(isNull).To(BeFalse())
		_, err = create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		var credential model.CertificateSigningRequest
		Expect(db.Where("org_id = ? AND name = ?", orgID, "credential").Take(&credential).Error).To(Succeed())
		Expect(credential.PolicySetRevision).To(Equal(int64(1)))
	})
	It("When migrating a nullable policy revision it should backfill before enforcing the constraint", func() {
		migrationDB, err := store.InitMigrationDB(cfg, log)
		Expect(err).NotTo(HaveOccurred())
		defer testdb.CloseDB(migrationDB)
		Expect(migrationDB.WithContext(ctx).Exec("ALTER TABLE certificate_signing_requests ALTER COLUMN policy_set_revision DROP NOT NULL").Error).To(Succeed())
		Expect(db.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", orgID, "credential").UpdateColumn("policy_set_revision", nil).Error).To(Succeed())
		credentials := csrstore.NewCertificateSigningRequestStore(migrationDB, log, "enrollment")
		Expect(credentials.InitialMigration(ctx)).To(Succeed())
		_, err = create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		var credential model.CertificateSigningRequest
		Expect(db.Where("org_id = ? AND name = ?", orgID, "credential").Take(&credential).Error).To(Succeed())
		Expect(credential.PolicySetRevision).To(Equal(int64(1)))
	})
	It("When validation fails it should leave no policy or revision writes", func() {
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
		Expect(db.Model(&model.EnrollmentApprovalPolicy{}).Where("org_id = ? AND name = ?", orgID, "west").Update("status", model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: 9})).Error).To(Succeed())
		_, err = policies.Get(ctx, uuid.New(), "west")
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		_, _, err = policies.Mutate(ctx, orgID, "west", "credential", policystore.Delete, func(_ *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			return nil, nil
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = policies.Get(ctx, orgID, "west")
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		var retained model.EnrollmentApprovalPolicy
		Expect(db.Unscoped().Where("org_id = ? AND name = ?", orgID, "west").Take(&retained).Error).To(Succeed())
		Expect(retained.Status.Data.EnrollmentCount).To(Equal(int64(9)))
		_, err = create("west", nil)
		Expect(err).To(MatchError(flterrors.ErrDuplicateName))
	})
	It("When concurrent policies select one credential each validation should see committed enabled siblings", func() {
		const total = 6
		start := make(chan struct{})
		observations := make(chan int, total)
		failures := make(chan error, total)
		var workers sync.WaitGroup
		for index := range total {
			workers.Add(1)
			go func(index int) {
				defer workers.Done()
				<-start
				name := string(rune('a' + index))
				_, _, err := policies.Mutate(ctx, orgID, name, "credential", policystore.Create, func(_ *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, enabled []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
					observations <- len(enabled)
					return &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: &name}, Spec: domain.EnrollmentApprovalPolicySpec{Enabled: true, CertificateName: "credential", MatchExpression: "true", DeviceLabels: map[string]string{}}}, nil
				})
				failures <- err
			}(index)
		}
		close(start)
		workers.Wait()
		close(observations)
		close(failures)
		for err := range failures {
			Expect(err).NotTo(HaveOccurred())
		}
		var counts []int
		for count := range observations {
			counts = append(counts, count)
		}
		sort.Ints(counts)
		Expect(counts).To(Equal([]int{0, 1, 2, 3, 4, 5}))
		var credential model.CertificateSigningRequest
		Expect(db.Where("org_id = ? AND name = ?", orgID, "credential").Take(&credential).Error).To(Succeed())
		Expect(credential.PolicySetRevision).To(Equal(int64(total)))
	})
	It("When updating policy configuration it should preserve count and reject stale versions", func() {
		created, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Model(&model.EnrollmentApprovalPolicy{}).Where("org_id = ? AND name = ?", orgID, "west").Update("status", model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: 9})).Error).To(Succeed())
		updated, _, err := policies.Mutate(ctx, orgID, "west", "credential", policystore.Update, func(current *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			current.Spec.Enabled = true
			current.Status = &domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: 0}
			return current, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Status.EnrollmentCount).To(Equal(int64(9)))
		Expect(*updated.Metadata.ResourceVersion).To(Equal("2"))
		_, _, err = policies.Mutate(ctx, orgID, "west", "credential", policystore.Update, func(_ *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			return created, nil
		})
		Expect(err).To(MatchError(flterrors.ErrResourceVersionConflict))
	})
	It("When a mutation changes nested spec maps it should retain the before snapshot and advance generation", func() {
		_, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		updated, previous, err := policies.Mutate(ctx, orgID, "west", "credential", policystore.Update, func(current *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			current.Spec.DeviceLabels["region"] = "west"
			return current, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(previous.Spec.DeviceLabels).To(BeEmpty())
		Expect(updated.Spec.DeviceLabels).To(HaveKeyWithValue("region", "west"))
		Expect(*updated.Metadata.Generation).To(Equal(int64(2)))
	})
	It("When the configured signer changes live and retained policy bindings should still protect the credential", func() {
		_, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		credentials := csrstore.NewCertificateSigningRequestStore(db, log, "changed-signer")
		for _, deletedPolicy := range []bool{false, true} {
			if deletedPolicy {
				_, _, err = policies.Mutate(ctx, orgID, "west", "credential", policystore.Delete, func(_ *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
					return nil, nil
				})
				Expect(err).NotTo(HaveOccurred())
			}
			_, err = credentials.Delete(ctx, orgID, "credential")
			Expect(err).To(MatchError(flterrors.ErrEnrollmentCredentialRetained))
			credential, err := credentials.Get(ctx, orgID, "credential")
			Expect(err).NotTo(HaveOccurred())
			_, _, err = credentials.Update(ctx, orgID, credential)
			Expect(err).To(MatchError(flterrors.ErrEnrollmentCredentialRetained))
			_, _, _, err = credentials.CreateOrUpdate(ctx, orgID, credential)
			Expect(err).To(MatchError(flterrors.ErrEnrollmentCredentialRetained))
		}
	})
	It("When same-version policy updates race exactly one should commit", func() {
		created, err := create("west", nil)
		Expect(err).NotTo(HaveOccurred())
		results := make(chan error, 2)
		start := make(chan struct{})
		for range 2 {
			go func() {
				<-start
				_, _, err := policies.Mutate(ctx, orgID, "west", "credential", policystore.Update, func(current *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
					current.Metadata.ResourceVersion = created.Metadata.ResourceVersion
					current.Spec.Enabled = true
					return current, nil
				})
				results <- err
			}()
		}
		close(start)
		first, second := <-results, <-results
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
		Expect(err).To(MatchError(flterrors.ErrEnrollmentCredentialRetained))
	})
	It("When binding creation races credential deletion it should never leave an orphan", func() {
		credentials := csrstore.NewCertificateSigningRequestStore(db, log, "enrollment")
		created := make(chan error, 1)
		deleted := make(chan error, 1)
		start := make(chan struct{})
		go func() { <-start; _, err := create("west", nil); created <- err }()
		go func() { <-start; _, err := credentials.Delete(ctx, orgID, "credential"); deleted <- err }()
		close(start)
		createError, deleteError := <-created, <-deleted
		if createError == nil {
			Expect(deleteError).To(MatchError(flterrors.ErrEnrollmentCredentialRetained))
			_, err := credentials.Get(ctx, orgID, "credential")
			Expect(err).NotTo(HaveOccurred())
		} else {
			Expect(createError).To(MatchError(flterrors.ErrResourceNotFound))
			Expect(deleteError).NotTo(HaveOccurred())
			_, err := policies.Get(ctx, orgID, "west")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		}
	})
})
