package store_test

import (
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

var _ = Describe("Persisted certificate creation metadata", func() {
	var suite *certificatePersistenceSuite
	var credentials csrstore.Store
	storedTime := time.Date(2026, 10, 18, 12, 0, 0, 123456000, time.UTC)
	BeforeEach(func() {
		suite = newCertificatePersistenceSuite()
		database := suite.DB.Session(&gorm.Session{NowFunc: func() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 123456789, time.UTC) }})
		credentials = csrstore.NewCertificateSigningRequestStore(database, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
	})

	It("When creating a CSR it should return its stored timestamp and accept an immediate status update", func() {
		request := newStoredCSR()
		created, err := credentials.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.Metadata.CreationTimestamp).To(Equal(&storedTime))
		loaded, err := credentials.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Metadata.CreationTimestamp).To(Equal(created.Metadata.CreationTimestamp))
		created.Status.Conditions = []domain.Condition{{
			Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue,
			Reason: "Approved", Message: "Approved for issuance",
		}}
		updated, err := seedCSRStatus(credentials, suite.Ctx, suite.OrgID, created)
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.IsStatusConditionTrue(updated.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved)).To(BeTrue())
		loaded, err = credentials.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Status).To(Equal(updated.Status))
	})

})
