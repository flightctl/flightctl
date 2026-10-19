package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Indexed retained enrollment leaves", func() {
	var suite *ServiceTestSuite
	BeforeEach(func() { suite = NewServiceTestSuite(); suite.Setup() })
	AfterEach(func() { suite.Teardown() })

	It("When a digest candidate has a different leaf it should not create false uniqueness or cross tenants", func() {
		request := CreateTestCSR()
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(util.WithOrganizationID(suite.Ctx, suite.OrgID), suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		leaf, err := fcrypto.ParseCertificatePEM(*created.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		other := CreateTestCSR()
		other.Spec.SignerName = "example.org/management"
		candidate, err := provider.Create(suite.Ctx, suite.OrgID, &other)
		Expect(err).NotTo(HaveOccurred())
		parsed, err := fcrypto.ParseCSR(other.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		different, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		Expect(different.Raw).NotTo(Equal(leaf.Raw))
		candidate.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: different.Raw}))
		_, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, candidate)
		Expect(err).NotTo(HaveOccurred())
		digest := sha256.Sum256(leaf.Raw)
		Expect(suite.DB.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *candidate.Metadata.Name).Update("certificate_digest", hex.EncodeToString(digest[:])).Error).To(Succeed())
		resolved, err := suite.CertificateSigningRequest.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(created.Metadata.Name))
		resolved, err = suite.CertificateSigningRequest.FindNameByCertificate(suite.Ctx, uuid.New(), leaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(BeNil())
		cancelled, cancel := context.WithCancel(suite.Ctx)
		cancel()
		_, err = suite.CertificateSigningRequest.FindNameByCertificate(cancelled, suite.OrgID, leaf.Raw)
		Expect(err).To(HaveOccurred())
		Expect(suite.DB.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *candidate.Metadata.Name).
			Update("status", model.MakeJSONField(map[string]any{"certificate": "!bad-base64!"})).Error).To(Succeed())
		resolved, err = suite.CertificateSigningRequest.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
		Expect(err).To(HaveOccurred())
		Expect(resolved).To(BeNil())
	})

	DescribeTable("When a retained enrollment signer outlives a spec change it should preserve its key and known or unknown issuance time", func(knownTime, concurrent bool) {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		request := CreateTestCSR()
		request.Spec.SignerName = "example.org/enrollment"
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600, signer.WithExtension(signer.OIDSignerName, "example.org/enrollment"))
		Expect(err).NotTo(HaveOccurred())
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
		created.Status.Certificate = &certificate
		var expectedTime *time.Time
		if knownTime {
			expectedTime = lo.ToPtr(time.Date(2025, 3, 4, 5, 6, 7, 123456000, time.UTC))
		}
		created.Status.IssuedAt = expectedTime
		if concurrent {
			created.Status.Certificate = lo.ToPtr([]byte{})
			created.Status.IssuedAt = nil
		}
		published, err := seedCSRStatus(provider, suite.Ctx, suite.OrgID, created)
		Expect(err).NotTo(HaveOccurred())
		published.Spec.SignerName = suite.caClient.Cfg.DeviceManagementSignerName
		updated, _, err := provider.Update(suite.Ctx, suite.OrgID, published)
		Expect(err).NotTo(HaveOccurred())
		updated.Status.Certificate = lo.ToPtr([]byte("losing replacement"))
		updated.Status.IssuedAt = lo.ToPtr(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
		updated.Status.Conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}}
		var persistence csrstore.Store = provider
		if concurrent {
			persistence = &concurrentCSRPublication{Store: provider, concurrentWrite: func(_ *domain.CertificateSigningRequest) error {
				winner, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
				if err != nil {
					return err
				}
				winner.Status.Certificate = &certificate
				winner.Status.IssuedAt = expectedTime
				_, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, winner)
				return err
			}}
		}
		retained, err := approveCSRConditions(suite.csrServiceWithSigner(persistence, "example.org/enrollment"), suite.Ctx, suite.OrgID, updated)
		Expect(err).NotTo(HaveOccurred())
		Expect(retained.Status.Certificate).To(Equal(&certificate))
		Expect(retained.Status.IssuedAt).To(Equal(expectedTime))
		Expect(domain.IsStatusConditionTrue(retained.Status.Conditions, domain.ConditionTypeCertificateSigningRequestDenied)).To(BeTrue())
		current, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Certificate).To(Equal(&certificate))
		Expect(current.Status.IssuedAt).To(Equal(expectedTime))
		Expect(domain.IsStatusConditionTrue(current.Status.Conditions, domain.ConditionTypeCertificateSigningRequestDenied)).To(BeTrue())
		Expect(provider.UpdateConditions(suite.Ctx, suite.OrgID, *retained.Metadata.Name, []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}})).To(Succeed())
		resolved, err := suite.CertificateSigningRequest.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(retained.Metadata.Name))
	}, Entry("known first issuance time", true, false), Entry("historically unknown issuance time", false, false),
		Entry("known concurrent first issuance time", true, true), Entry("historically unknown concurrent issuance time", false, true))
})
