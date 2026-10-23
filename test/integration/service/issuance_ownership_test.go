package service_test

import (
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
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
		provider = csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
	})
	AfterEach(func() { suite.Teardown() })

	It("When approving non-enrollment status it should normalize issuance time and preserve stored certificate ownership", func() {
		request := CreateTestCSR()
		request.Spec.SignerName = suite.caClient.Cfg.DeviceManagementSignerName
		published, err := seedPreFeatureCertificate(provider, suite.Ctx, suite.OrgID, request, []byte("stored-management-certificate"), lo.ToPtr(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)))
		Expect(err).NotTo(HaveOccurred())
		published.Status.Certificate = lo.ToPtr([]byte("forged-management-certificate"))
		published.Status.Conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}}
		updated, err := approveCSRConditions(suite.CertificateSigningRequest, suite.Ctx, suite.OrgID, published)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Status.IssuedAt).To(BeNil())
		Expect(string(*updated.Status.Certificate)).To(Equal("stored-management-certificate"))
		Expect(domain.IsStatusConditionTrue(updated.Status.Conditions, domain.ConditionTypeCertificateSigningRequestDenied)).To(BeTrue())
		loaded, getStatus := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *updated.Metadata.Name)
		Expect(getStatus.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(loaded.Status).To(Equal(updated.Status))
	})

	It("When approval signing fails for historical non-enrollment status it should persist the old failure reason without issuance time", func() {
		request := CreateTestCSR()
		request.Spec.SignerName = suite.caClient.Cfg.DeviceManagementSignerName
		request.Metadata.Name = lo.ToPtr(*request.Metadata.Name + "-mismatch")
		created, err := seedPreFeatureCertificate(provider, suite.Ctx, suite.OrgID, request, nil, lo.ToPtr(time.Date(2024, 3, 2, 1, 0, 0, 0, time.UTC)))
		Expect(err).NotTo(HaveOccurred())
		created.Status.Conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue, Reason: "Approved"}}
		_, status := suite.CertificateSigningRequest.UpdateCertificateSigningRequestApproval(suite.Ctx, suite.OrgID, *created.Metadata.Name, *created)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		loaded, getStatus := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(getStatus.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(loaded.Status.IssuedAt).To(BeNil())
		Expect(loaded.Status.Certificate).To(BeNil())
		Expect(domain.FindStatusCondition(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed).Reason).To(Equal("SigningFailed"))
	})

	DescribeTable("When the signer spec changes it should preserve actual retained custom enrollment history", func(issuedAt *time.Time) {
		request := CreateTestCSR()
		request.Spec.SignerName = "example.org/bootstrap"
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, "example.org/bootstrap"))
		Expect(err).NotTo(HaveOccurred())
		certificate, err := fcrypto.EncodeCertificatePEM(leaf)
		Expect(err).NotTo(HaveOccurred())
		published, err := seedPreFeatureCertificate(provider, suite.Ctx, suite.OrgID, request, certificate, issuedAt)
		Expect(err).NotTo(HaveOccurred())
		published.Spec.SignerName = "flightctl.io/device-enrollment"
		changed, err := replaceHistoricalCSRSigner(provider, suite.Ctx, suite.OrgID, published)
		Expect(err).NotTo(HaveOccurred())
		changed.Status.Certificate = lo.ToPtr([]byte("uncommitted-replacement"))
		changed.Status.IssuedAt = lo.ToPtr(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
		changed.Status.Conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}}
		updated, err := approveCSRConditions(suite.csrServiceWithSigner(provider, "example.org/bootstrap"), suite.Ctx, suite.OrgID, changed)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Status.Certificate).To(Equal(&certificate))
		Expect(updated.Status.IssuedAt).To(Equal(issuedAt))
	}, Entry("known historical time", lo.ToPtr(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC))), Entry("unknown historical time", (*time.Time)(nil)))
})
