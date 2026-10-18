package service_test

import (
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Enrollment issuance field ownership", func() {
	var suite *ServiceTestSuite
	var provider csrstore.Store
	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		suite.Ctx = util.WithOrganizationID(suite.Ctx, suite.OrgID)
		provider = csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, "example.org/bootstrap")
	})
	AfterEach(func() { suite.Teardown() })

	It("When updating non-enrollment status it should discard supplied issuance time without freezing other fields", func() {
		request := CreateTestCSR()
		request.Spec.SignerName = "flightctl.io/device-enrollment"
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		created.Status.Certificate = lo.ToPtr([]byte("first-management-certificate"))
		created.Status.IssuedAt = lo.ToPtr(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		updated, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, created)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Status.IssuedAt).To(BeNil())
		updated.Status.Certificate = lo.ToPtr([]byte("replacement-management-certificate"))
		updated.Status.IssuedAt = lo.ToPtr(time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC))
		updated.Status.Conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue, Reason: "Approved"}}
		updated, err = provider.UpdateStatus(suite.Ctx, suite.OrgID, updated)
		Expect(err).NotTo(HaveOccurred())
		loaded, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Status.IssuedAt).To(BeNil())
		Expect(string(*loaded.Status.Certificate)).To(Equal("replacement-management-certificate"))
		Expect(domain.IsStatusConditionTrue(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved)).To(BeTrue())
		Expect(loaded.Status).To(Equal(updated.Status))
	})

	DescribeTable("When writing conditions on historical non-enrollment status it should normalize time only on an actual write", func(change bool) {
		request := CreateTestCSR()
		request.Spec.SignerName = "flightctl.io/device-enrollment"
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		oldTime := time.Date(2024, 3, 2, 1, 0, 0, 0, time.UTC)
		created.Status.IssuedAt = &oldTime
		result := suite.DB.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *created.Metadata.Name).
			Update("status", model.MakeJSONField(*created.Status))
		Expect(result.Error).NotTo(HaveOccurred())
		Expect(result.RowsAffected).To(Equal(int64(1)))
		conditions := []domain.Condition{}
		if change {
			conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}}
		}
		Expect(provider.UpdateConditions(suite.Ctx, suite.OrgID, *created.Metadata.Name, conditions)).To(Succeed())
		loaded, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		if change {
			Expect(loaded.Status.IssuedAt).To(BeNil())
			Expect(domain.IsStatusConditionTrue(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestDenied)).To(BeTrue())
		} else {
			Expect(loaded.Status.IssuedAt).To(Equal(&oldTime))
			Expect(loaded.Metadata.ResourceVersion).To(Equal(created.Metadata.ResourceVersion))
		}
	}, Entry("changed conditions", true), Entry("no-op conditions", false))

	It("When an unchanged condition follows a changed condition it should persist the change and normalize non-enrollment time", func() {
		request := CreateTestCSR()
		request.Spec.SignerName = "flightctl.io/device-enrollment"
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		unchanged := domain.Condition{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusFalse, Reason: "Pending"}
		created.Status.Conditions = []domain.Condition{unchanged}
		created, err = provider.UpdateStatus(suite.Ctx, suite.OrgID, created)
		Expect(err).NotTo(HaveOccurred())
		oldTime := time.Date(2024, 3, 2, 1, 0, 0, 0, time.UTC)
		created.Status.IssuedAt = &oldTime
		result := suite.DB.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *created.Metadata.Name).
			Update("status", model.MakeJSONField(*created.Status))
		Expect(result.Error).NotTo(HaveOccurred())
		Expect(result.RowsAffected).To(Equal(int64(1)))
		conditions := []domain.Condition{
			{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"},
			unchanged,
		}
		Expect(provider.UpdateConditions(suite.Ctx, suite.OrgID, *created.Metadata.Name, conditions)).To(Succeed())
		loaded, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.IsStatusConditionTrue(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestDenied)).To(BeTrue())
		Expect(domain.FindStatusCondition(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved).Reason).To(Equal("Pending"))
		Expect(loaded.Status.IssuedAt).To(BeNil())
		Expect(loaded.Metadata.ResourceVersion).NotTo(Equal(created.Metadata.ResourceVersion))
		Expect(provider.UpdateConditions(suite.Ctx, suite.OrgID, *created.Metadata.Name, conditions)).To(Succeed())
		repeated, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(repeated.Status).To(Equal(loaded.Status))
		Expect(repeated.Metadata.ResourceVersion).To(Equal(loaded.Metadata.ResourceVersion))
	})

	It("When approval signing fails for historical non-enrollment status it should persist the old failure reason without issuance time", func() {
		request := CreateTestCSR()
		request.Spec.SignerName = suite.caClient.Cfg.DeviceManagementSignerName
		request.Metadata.Name = lo.ToPtr(*request.Metadata.Name + "-mismatch")
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		created.Status.IssuedAt = lo.ToPtr(time.Date(2024, 3, 2, 1, 0, 0, 0, time.UTC))
		result := suite.DB.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *created.Metadata.Name).
			Update("status", model.MakeJSONField(*created.Status))
		Expect(result.Error).NotTo(HaveOccurred())
		created.Status.Conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue, Reason: "Approved"}}
		_, status := suite.CertificateSigningRequest.UpdateCertificateSigningRequestApproval(suite.Ctx, suite.OrgID, *created.Metadata.Name, *created)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		loaded, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Status.IssuedAt).To(BeNil())
		Expect(loaded.Status.Certificate).To(BeNil())
		Expect(domain.FindStatusCondition(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed).Reason).To(Equal("SigningFailed"))
	})

	DescribeTable("When the signer spec changes it should preserve actual retained custom enrollment history", func(issuedAt *time.Time) {
		request := CreateTestCSR()
		request.Spec.SignerName = "example.org/bootstrap"
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, "example.org/bootstrap"))
		Expect(err).NotTo(HaveOccurred())
		certificate, err := fcrypto.EncodeCertificatePEM(leaf)
		Expect(err).NotTo(HaveOccurred())
		created.Status.Certificate = &certificate
		created.Status.IssuedAt = issuedAt
		published, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, created)
		Expect(err).NotTo(HaveOccurred())
		published.Spec.SignerName = "flightctl.io/device-enrollment"
		changed, _, err := provider.Update(suite.Ctx, suite.OrgID, published)
		Expect(err).NotTo(HaveOccurred())
		changed.Status.Certificate = lo.ToPtr([]byte("uncommitted-replacement"))
		changed.Status.IssuedAt = lo.ToPtr(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
		updated, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, changed)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Status.Certificate).To(Equal(&certificate))
		Expect(updated.Status.IssuedAt).To(Equal(issuedAt))
	}, Entry("known historical time", lo.ToPtr(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC))), Entry("unknown historical time", (*time.Time)(nil)))
})
