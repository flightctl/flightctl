package service_test

import (
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/service/tpmcsr"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/util"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

var _ = Describe("Persisted creation timestamps", func() {
	var suite *ServiceTestSuite
	var database *gorm.DB
	var credentials csrstore.Store
	var service *csrservice.ServiceHandler
	storedTime := time.Date(2026, 10, 18, 12, 0, 0, 123456000, time.UTC)

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		suite.Ctx = util.WithOrganizationID(suite.Ctx, suite.OrgID)
		database = suite.DB.Session(&gorm.Session{NowFunc: func() time.Time {
			return time.Date(2026, 10, 18, 12, 0, 0, 123456789, time.UTC)
		}})
		credentials = csrstore.NewCertificateSigningRequestStore(database, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		service = csrservice.NewServiceHandler(credentials, tpmcsr.NewVerifier(suite.EnrollmentRequest), suite.caClient,
			events.NewServiceHandler(suite.EventStore, suite.workerClient, suite.Log), suite.Log, "", "")
	})

	AfterEach(func() {
		suite.Teardown()
	})

	It("When signing a fresh CSR it should persist approval and issuance without a false incarnation conflict", func() {
		request := CreateTestCSR()
		created, status := service.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Metadata.CreationTimestamp).To(Equal(&storedTime))
		Expect(domain.IsStatusConditionTrue(created.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved)).To(BeTrue())
		Expect(created.Status.Certificate).NotTo(BeNil())
		_, err := fcrypto.ParseCertificatePEM(*created.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		loaded, status := service.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(loaded.Status).To(Equal(created.Status))
	})

	It("When signing fails for a fresh CSR it should persist the existing signing failure condition", func() {
		request := CreateTestCSR()
		request.Metadata.Name = lo.ToPtr(*request.Metadata.Name + "-mismatch")
		_, status := service.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		loaded, status := service.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(domain.IsStatusConditionTrue(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved)).To(BeTrue())
		Expect(domain.IsStatusConditionTrue(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed)).To(BeTrue())
		Expect(domain.FindStatusCondition(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed).Reason).To(Equal("SigningFailed"))
		Expect(loaded.Status.Certificate).To(BeNil())
	})

	It("When generating an image enrollment credential it should publish a usable certificate", func() {
		credential, status := service.GenerateEnrollmentCredential(suite.Ctx, suite.OrgID, "precision-image", "ImageBuild", "precision-build")
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(credential).NotTo(BeNil())
		_, err := fcrypto.ParseCertificatePEM(credential.CertificatePEM)
		Expect(err).NotTo(HaveOccurred())
		loaded, status := service.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, credential.CSRName)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(loaded.Metadata.CreationTimestamp).To(Equal(&storedTime))
		Expect(loaded.Status.Certificate).To(Equal(&credential.CertificatePEM))
	})

})
