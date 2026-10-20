package store_test

import (
	"encoding/pem"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Historical certificate storage deletion", func() {
	DescribeTable("When deleting issued CSR names it should allow name reuse for every signer",
		func(signerName string, changeSigner bool) {
			suite := newCertificatePersistenceSuite()
			provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
			request := newStoredCSR()
			request.Spec.SignerName = signerName
			stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
			Expect(err).NotTo(HaveOccurred())
			parsed, err := fcrypto.ParseCSR(request.Spec.Request)
			Expect(err).NotTo(HaveOccurred())
			leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
				signer.WithExtension(signer.OIDSignerName, signerName))
			Expect(err).NotTo(HaveOccurred())
			stored.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))
			published, err := seedCSRStatus(provider, suite.Ctx, suite.OrgID, stored)
			Expect(err).NotTo(HaveOccurred())
			Expect(published.Status.IssuedAt).To(BeNil())
			if changeSigner {
				published.Spec.SignerName = "flightctl.io/device-enrollment"
				_, _, err = provider.Update(suite.Ctx, suite.OrgID, published)
				Expect(err).NotTo(HaveOccurred())
			}
			deleted, err := provider.Delete(suite.Ctx, suite.OrgID, *request.Metadata.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(deleted).To(BeTrue())
			recreated, err := provider.Create(suite.Ctx, suite.OrgID, &request)
			Expect(err).NotTo(HaveOccurred())
			Expect(recreated.Status.Certificate).To(BeNil())
			Expect(recreated.Status.IssuedAt).To(BeNil())
		},
		Entry("configured enrollment signer, historical leaf without identity metadata", "example.org/bootstrap", false),
		Entry("issued enrollment credential after a signer-spec change", "example.org/bootstrap", true),
		Entry("default name when enrollment is configured differently", "flightctl.io/enrollment", false),
		Entry("management signer", "flightctl.io/device-enrollment", false),
	)
})
