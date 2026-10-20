package service_test

import (
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/service/tpmcsr"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	erstore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
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
		credentials = csrstore.NewCertificateSigningRequestStore(database, suite.Log)
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
		Expect(created.Status.IssuedAt).NotTo(BeNil())
		leaf, err := fcrypto.ParseCertificatePEM(*created.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		Expect(enrollmentIdentityFromCertificate(leaf)).To(Equal(*request.Metadata.Name))
		loaded, err := credentials.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Status).To(Equal(created.Status))
	})

	It("When signing fails for a fresh CSR it should persist the existing signing failure condition", func() {
		request := CreateTestCSR()
		request.Metadata.Name = lo.ToPtr(*request.Metadata.Name + "-mismatch")
		_, status := service.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		loaded, err := credentials.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.IsStatusConditionTrue(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved)).To(BeTrue())
		Expect(domain.IsStatusConditionTrue(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed)).To(BeTrue())
		Expect(domain.FindStatusCondition(loaded.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed).Reason).To(Equal("SigningFailed"))
		Expect(loaded.Status.Certificate).To(BeNil())
		Expect(loaded.Status.IssuedAt).To(BeNil())
	})

	It("When generating an image enrollment credential it should publish a usable certificate", func() {
		credential, status := service.GenerateEnrollmentCredential(suite.Ctx, suite.OrgID, "precision-image", "ImageBuild", "precision-build")
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(credential).NotTo(BeNil())
		leaf, err := fcrypto.ParseCertificatePEM(credential.CertificatePEM)
		Expect(err).NotTo(HaveOccurred())
		Expect(enrollmentIdentityFromCertificate(leaf)).To(Equal(credential.CSRName))
		loaded, err := credentials.Get(suite.Ctx, suite.OrgID, credential.CSRName)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Metadata.CreationTimestamp).To(Equal(&storedTime))
		Expect(loaded.Status.Certificate).To(Equal(&credential.CertificatePEM))
	})

	DescribeTable("When updating a freshly created ER it should preserve its original origin and other status", func(origin *string, mutate bool) {
		provider := erstore.NewEnrollmentRequestStore(database, suite.Log)
		request := CreateTestER()
		request.Status = &domain.EnrollmentRequestStatus{EnrollmentCertificateName: origin, Conditions: []domain.Condition{}}
		var created *domain.EnrollmentRequest
		var err error
		if mutate {
			var wasCreated bool
			created, _, wasCreated, err = provider.Mutate(suite.Ctx, suite.OrgID, *request.Metadata.Name, nil, func(mutation *store.Mutation[domain.EnrollmentRequest]) error {
				mutation.SetResource(&request)
				return nil
			})
			Expect(wasCreated).To(BeTrue())
		} else {
			created, err = provider.Create(suite.Ctx, suite.OrgID, &request)
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(created.Metadata.CreationTimestamp).To(Equal(&storedTime))
		created.Status.EnrollmentCertificateName = lo.ToPtr("replacement-origin")
		created.Status.Conditions = []domain.Condition{{Type: "Pending", Status: domain.ConditionStatusTrue, Reason: "AwaitingApproval", Message: "Awaiting approval"}}
		updated, status := suite.enrollmentServiceWithPersistence(credentials, provider).ReplaceEnrollmentRequestStatus(suite.Ctx, suite.OrgID, *created.Metadata.Name, *created)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(updated.Status.EnrollmentCertificateName).To(Equal(origin))
		Expect(updated.Status.Conditions).To(Equal(created.Status.Conditions))
		loaded, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Metadata.CreationTimestamp).To(Equal(&storedTime))
		Expect(loaded.Status).To(Equal(updated.Status))
	},
		Entry("known origin from Create", lo.ToPtr("precision-credential"), false),
		Entry("absent origin from Create", (*string)(nil), false),
		Entry("known origin from Mutate creation", lo.ToPtr("precision-credential"), true),
		Entry("absent origin from Mutate creation", (*string)(nil), true),
	)
})
