package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/consts"
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
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, suite.caClient.Cfg.DeviceEnrollmentSignerName),
			signer.WithExtension(signer.OIDOrgID, suite.OrgID.String()))
		Expect(err).NotTo(HaveOccurred())
		digest := sha256.Sum256(leaf.Raw)
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
			_, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, stored)
			Expect(err).NotTo(HaveOccurred())
			Expect(suite.DB.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *stored.Metadata.Name).Update("certificate_digest", hex.EncodeToString(digest[:])).Error).To(Succeed())
		}
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(BeNil())
	})

	It("When retained certificate JSON cannot decode as bytes it should return a storage error rather than a unique identity", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, suite.caClient.Cfg.DeviceEnrollmentSignerName),
			signer.WithExtension(signer.OIDOrgID, suite.OrgID.String()))
		Expect(err).NotTo(HaveOccurred())
		stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		stored.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))
		stored, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, stored)
		Expect(err).NotTo(HaveOccurred())
		result := suite.DB.Model(&model.CertificateSigningRequest{}).
			Where("org_id = ? AND name = ?", suite.OrgID, *stored.Metadata.Name).
			UpdateColumn("status", gorm.Expr("?::jsonb", `{"certificate":"not-base64"}`))
		Expect(result.Error).NotTo(HaveOccurred())
		Expect(result.RowsAffected).To(Equal(int64(1)))
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusInternalServerError))
		Expect(created).To(BeNil())
	})
})
