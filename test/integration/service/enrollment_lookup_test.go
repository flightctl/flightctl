package service_test

import (
	"encoding/pem"
	"fmt"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

var _ = Describe("Retained enrollment leaf lookup", func() {
	var suite *ServiceTestSuite
	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})
	AfterEach(func() { suite.Teardown() })

	It("When a second retained match follows many candidates it should report ambiguity without a cutoff", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, suite.caClient.Cfg.DeviceEnrollmentSignerName))
		Expect(err).NotTo(HaveOccurred())
		for index := 0; index < 130; index++ {
			candidate := request
			candidate.Metadata.Name = lo.ToPtr(fmt.Sprintf("candidate-%03d", index))
			stored, err := provider.Create(suite.Ctx, suite.OrgID, &candidate)
			Expect(err).NotTo(HaveOccurred())
			certificate := []byte("malformed PEM")
			if index == 0 || index == 129 {
				certificate = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
			}
			stored.Status.Certificate = &certificate
			_, err = provider.UpdateStatus(suite.Ctx, suite.OrgID, stored)
			Expect(err).NotTo(HaveOccurred())
		}
		name, err := provider.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(BeNil())
	})

	It("When retained certificate JSON cannot decode as bytes it should return a storage error rather than a unique identity", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		request := CreateTestCSR()
		stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		result := suite.DB.Model(&model.CertificateSigningRequest{}).
			Where("org_id = ? AND name = ?", suite.OrgID, *stored.Metadata.Name).
			UpdateColumn("status", gorm.Expr("?::jsonb", `{"certificate":"not-base64"}`))
		Expect(result.Error).NotTo(HaveOccurred())
		Expect(result.RowsAffected).To(Equal(int64(1)))
		name, err := provider.FindNameByCertificate(suite.Ctx, suite.OrgID, []byte("presented leaf"))
		Expect(err).To(MatchError(ContainSubstring("decode retained certificate")))
		Expect(name).To(BeNil())
	})
})
