package service_test

import (
	"context"
	"net/http"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	authcommon "github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

type enrollmentIdentityWithCSRName struct {
	*authcommon.BaseIdentity
	csrName *string
}

func (i *enrollmentIdentityWithCSRName) GetEnrollmentCSRName() *string {
	return i.csrName
}

var _ = Describe("Enrollment credential identity", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		suite.Ctx = util.WithOrganizationID(suite.Ctx, suite.OrgID)
	})

	AfterEach(func() {
		suite.Teardown()
	})

	It("When issuing an enrollment credential it should persist its identity and issuance time", func() {
		request := CreateTestCSR()
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.Certificate).NotTo(BeNil())
		Expect(created.Status.IssuedAt).NotTo(BeNil())
		Expect(*created.Status.IssuedAt).To(BeTemporally("~", time.Now().UTC(), time.Minute))

		loaded, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(loaded.Status).To(Equal(created.Status))
	})

	It("When enrollment authentication supplies a CSR name it should persist and protect it", func() {
		credentialName := "enrollment-credential"
		enrollmentIdentity := &enrollmentIdentityWithCSRName{
			BaseIdentity: authcommon.NewBaseIdentity("enrollment-agent", "enrollment-agent", nil),
			csrName:      &credentialName,
		}
		ctx := context.WithValue(suite.Ctx, consts.IdentityCtxKey, enrollmentIdentity)

		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(Equal(&credentialName))

		created.Status.EnrollmentCertificateName = lo.ToPtr("forged-origin")
		updated, status := suite.EnrollmentRequest.ReplaceEnrollmentRequestStatus(suite.Ctx, suite.OrgID, *created.Metadata.Name, *created)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(updated.Status.EnrollmentCertificateName).To(Equal(&credentialName))
	})

	It("When enrollment issuance races it should preserve the first committed evidence", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		request := CreateTestCSR()
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.Status.Certificate).To(BeNil())

		type approvalResult struct {
			csr    *api.CertificateSigningRequest
			status api.Status
		}
		const writers = 4
		start := make(chan struct{})
		results := make(chan approvalResult, writers)
		var wait sync.WaitGroup
		for range writers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				approval := *created
				approval.Status = &api.CertificateSigningRequestStatus{Conditions: []api.Condition{{
					Type:    api.ConditionTypeCertificateSigningRequestApproved,
					Status:  api.ConditionStatusTrue,
					Reason:  "Approved",
					Message: "Concurrent approval",
				}}}
				csr, status := suite.CertificateSigningRequest.UpdateCertificateSigningRequestApproval(
					suite.Ctx, suite.OrgID, lo.FromPtr(created.Metadata.Name), approval)
				results <- approvalResult{csr: csr, status: status}
			}()
		}
		close(start)
		wait.Wait()
		close(results)

		var successes []*api.CertificateSigningRequest
		for result := range results {
			if result.status.Code != http.StatusOK {
				Expect(result.status.Code).To(BeEquivalentTo(http.StatusConflict))
				continue
			}
			Expect(result.csr).NotTo(BeNil())
			Expect(result.csr.Status.Certificate).NotTo(BeNil())
			Expect(result.csr.Status.IssuedAt).NotTo(BeNil())
			successes = append(successes, result.csr)
		}
		Expect(successes).NotTo(BeEmpty())

		stored, err := provider.Get(suite.Ctx, suite.OrgID, lo.FromPtr(created.Metadata.Name))
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Status.Certificate).NotTo(BeNil())
		Expect(stored.Status.IssuedAt).NotTo(BeNil())
		for _, result := range successes {
			Expect(result.Status.Certificate).To(Equal(stored.Status.Certificate))
			Expect(result.Status.IssuedAt).To(Equal(stored.Status.IssuedAt))
		}
	})
})
