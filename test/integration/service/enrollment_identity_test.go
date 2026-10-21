package service_test

import (
	"context"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/util"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

func enrollmentCSRName(certificate *x509.Certificate) string {
	GinkgoHelper()
	var name string
	for _, extension := range certificate.Extensions {
		if !extension.Id.Equal(signer.OIDEnrollmentCSRName) {
			continue
		}
		rest, err := asn1.Unmarshal(extension.Value, &name)
		Expect(err).NotTo(HaveOccurred())
		Expect(rest).To(BeEmpty())
		return name
	}
	return ""
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

		leaf, err := fcrypto.ParseCertificatePEM(*created.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		Expect(enrollmentCSRName(leaf)).To(Equal(*request.Metadata.Name))

		loaded, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(loaded.Status).To(Equal(created.Status))
	})

	It("When an authenticated device creates an ER it should retain the attested credential reference", func() {
		credential := CreateTestCSR()
		issued, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, credential)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		leaf, err := fcrypto.ParseCertificatePEM(*issued.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)

		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(Equal(credential.Metadata.Name))

		created.Status.EnrollmentCertificateName = lo.ToPtr("forged-origin")
		updated, status := suite.EnrollmentRequest.ReplaceEnrollmentRequestStatus(suite.Ctx, suite.OrgID, *created.Metadata.Name, *created)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(updated.Status.EnrollmentCertificateName).To(Equal(credential.Metadata.Name))
	})

	It("When an old credential has no identity it should recover only a unique exact leaf match", func() {
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, suite.caClient.Cfg.DeviceEnrollmentSignerName),
			signer.WithExtension(signer.OIDOrgID, suite.OrgID.String()))
		Expect(err).NotTo(HaveOccurred())
		certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
		request.Status = &domain.CertificateSigningRequestStatus{
			Certificate: &certificate,
			Conditions:  []domain.Condition{},
		}
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)

		first, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(first.Status.EnrollmentCertificateName).To(Equal(created.Metadata.Name))

		duplicate := request
		duplicate.Metadata.Name = lo.ToPtr("duplicate-retained-leaf")
		_, err = provider.Create(suite.Ctx, suite.OrgID, &duplicate)
		Expect(err).NotTo(HaveOccurred())
		second, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(second.Status.EnrollmentCertificateName).To(BeNil())
	})
})
