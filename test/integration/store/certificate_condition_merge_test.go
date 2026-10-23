package store_test

import (
	"github.com/flightctl/flightctl/internal/domain"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Technical certificate condition merging", func() {
	var suite *certificatePersistenceSuite
	var provider csrstore.Store
	BeforeEach(func() {
		suite = newCertificatePersistenceSuite()
		provider = csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
	})

	DescribeTable("When writing conditions on historical non-enrollment status it should merge conditions without normalizing business fields", func(change bool) {
		request := newStoredCSR()
		request.Spec.SignerName = "flightctl.io/device-enrollment"
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		conditions := []domain.Condition{}
		if change {
			conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}}
		}
		Expect(provider.UpdateConditions(suite.Ctx, suite.OrgID, *created.Metadata.Name, conditions)).To(Succeed())
		loaded, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		if change {
			Expect(domain.IsStatusConditionTrue(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestDenied)).To(BeTrue())
		} else {
			Expect(loaded.Metadata.ResourceVersion).To(Equal(created.Metadata.ResourceVersion))
		}
	}, Entry("changed conditions", true), Entry("no-op conditions", false))

	It("When an unchanged condition follows a changed condition it should persist every changed condition", func() {
		request := newStoredCSR()
		request.Spec.SignerName = "flightctl.io/device-enrollment"
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		unchanged := domain.Condition{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusFalse, Reason: "Pending"}
		created.Status.Conditions = []domain.Condition{unchanged}
		created, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, created)
		Expect(err).NotTo(HaveOccurred())
		conditions := []domain.Condition{
			{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"},
			unchanged,
		}
		Expect(provider.UpdateConditions(suite.Ctx, suite.OrgID, *created.Metadata.Name, conditions)).To(Succeed())
		loaded, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.IsStatusConditionTrue(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestDenied)).To(BeTrue())
		Expect(domain.FindStatusCondition(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved).Reason).To(Equal("Pending"))
		Expect(loaded.Metadata.ResourceVersion).NotTo(Equal(created.Metadata.ResourceVersion))
		Expect(provider.UpdateConditions(suite.Ctx, suite.OrgID, *created.Metadata.Name, conditions)).To(Succeed())
		repeated, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(repeated.Status).To(Equal(loaded.Status))
		Expect(repeated.Metadata.ResourceVersion).To(Equal(loaded.Metadata.ResourceVersion))
	})

})
