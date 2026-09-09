package store_test

import (
	"context"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	enrollmenthookpolicystore "github.com/flightctl/flightctl/internal/store/enrollmenthookpolicy"
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

var _ = Describe("EnrollmentHookPolicyStore", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		ehpStore  enrollmenthookpolicystore.Store
		cfg       *config.Config
		dbName    string
		db        *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		ehpStore = enrollmenthookpolicystore.NewStore(db, log.WithField("pkg", "enrollmenthookpolicy-store"))
		organizationStore := organizationstore.NewOrganizationStore(db)
		orgId = uuid.New()
		err = testutil.CreateTestOrganization(ctx, organizationStore, orgId)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	It("When creating a policy it should succeed", func() {
		policy := newEnrollmentHookPolicy("default")
		result, err := ehpStore.Create(ctx, orgId, policy)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).ToNot(BeNil())
		Expect(lo.FromPtr(result.Metadata.Name)).To(Equal("default"))
	})

	It("When getting a created policy it should return it", func() {
		policy := newEnrollmentHookPolicy("default")
		_, err := ehpStore.Create(ctx, orgId, policy)
		Expect(err).ToNot(HaveOccurred())

		result, err := ehpStore.Get(ctx, orgId, "default")
		Expect(err).ToNot(HaveOccurred())
		Expect(result).ToNot(BeNil())
		Expect(lo.FromPtr(result.Metadata.Name)).To(Equal("default"))
	})

	It("When creating a duplicate it should fail with ErrDuplicateName", func() {
		policy := newEnrollmentHookPolicy("default")
		_, err := ehpStore.Create(ctx, orgId, policy)
		Expect(err).ToNot(HaveOccurred())

		_, err = ehpStore.Create(ctx, orgId, policy)
		Expect(err).To(HaveOccurred())
		Expect(err).To(MatchError(flterrors.ErrDuplicateName))
	})

	It("When deleting a policy it should succeed", func() {
		policy := newEnrollmentHookPolicy("default")
		_, err := ehpStore.Create(ctx, orgId, policy)
		Expect(err).ToNot(HaveOccurred())

		deleted, err := ehpStore.Delete(ctx, orgId, "default")
		Expect(err).ToNot(HaveOccurred())
		Expect(deleted).To(BeTrue())

		_, err = ehpStore.Get(ctx, orgId, "default")
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
	})

	It("When listing policies it should return all", func() {
		policy := newEnrollmentHookPolicy("default")
		_, err := ehpStore.Create(ctx, orgId, policy)
		Expect(err).ToNot(HaveOccurred())

		list, err := ehpStore.List(ctx, orgId, store.ListParams{})
		Expect(err).ToNot(HaveOccurred())
		Expect(list.Items).To(HaveLen(1))
	})

	It("When updating a policy via CreateOrUpdate it should succeed", func() {
		policy := newEnrollmentHookPolicy("default")
		_, err := ehpStore.Create(ctx, orgId, policy)
		Expect(err).ToNot(HaveOccurred())

		updated := newEnrollmentHookPolicy("default")
		updated.Spec.AfterEnrolling.FailurePolicy = lo.ToPtr(api.FailurePolicyContinue)
		result, _, _, err := ehpStore.CreateOrUpdate(ctx, orgId, updated)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).ToNot(BeNil())
	})
})

func newEnrollmentHookPolicy(name string) *api.EnrollmentHookPolicy {
	return &api.EnrollmentHookPolicy{
		ApiVersion: "flightctl.io/v1beta1",
		Kind:       api.EnrollmentHookPolicyKind,
		Metadata:   api.ObjectMeta{Name: &name},
		Spec: api.EnrollmentHookPolicySpec{
			AfterEnrolling: api.EnrollmentHookStageSpec{
				FailurePolicy: lo.ToPtr(api.FailurePolicyBlock),
				ControlPlaneActions: &[]api.EnrollmentHookHttpAction{
					{
						Url:     "https://example.com/hook",
						Timeout: lo.ToPtr("30s"),
					},
				},
			},
		},
	}
}
