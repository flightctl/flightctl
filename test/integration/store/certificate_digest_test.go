package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/selector"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

func findStoredCertificate(provider csrstore.Store, ctx context.Context, orgID uuid.UUID, leafDER []byte) (*string, error) {
	digest := sha256.Sum256(leafDER)
	filter := selector.NewFieldSelectorOrDie("internal.certificateDigest="+hex.EncodeToString(digest[:]), selector.WithPrivateSelectors())
	listed, err := provider.List(ctx, orgID, store.ListParams{FieldSelector: filter})
	if err != nil {
		return nil, err
	}
	if len(listed.Items) != 1 {
		return nil, nil
	}
	return listed.Items[0].Metadata.Name, nil
}

var _ = Describe("Certificate lookup key persistence", func() {
	var suite *certificatePersistenceSuite
	BeforeEach(func() { suite = newCertificatePersistenceSuite() })

	It("When status changes it should index each committed certificate replacement or clear", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		request := newStoredCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		first, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		second, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Raw).NotTo(Equal(first.Raw))
		for _, enrollment := range []bool{true, false} {
			resource := request
			resource.Spec.SignerName = "example.org/management"
			if enrollment {
				resource.Spec.SignerName = "example.org/enrollment"
			}
			resource.Metadata.Name = lo.ToPtr(uuid.NewString())
			created, err := provider.Create(suite.Ctx, suite.OrgID, &resource)
			Expect(err).NotTo(HaveOccurred())
			created.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: first.Raw}))
			published, err := seedCSRStatus(provider, suite.Ctx, suite.OrgID, created)
			Expect(err).NotTo(HaveOccurred())
			published.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: second.Raw}))
			updated, err := seedCSRStatus(provider, suite.Ctx, suite.OrgID, published)
			Expect(err).NotTo(HaveOccurred())
			Expect(provider.UpdateConditions(suite.Ctx, suite.OrgID, *updated.Metadata.Name, []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}})).To(Succeed())
			previous, err := findStoredCertificate(provider, suite.Ctx, suite.OrgID, first.Raw)
			Expect(err).NotTo(HaveOccurred())
			Expect(previous).To(BeNil())
			retained := second.Raw
			resolved, err := findStoredCertificate(provider, suite.Ctx, suite.OrgID, retained)
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved).To(Equal(updated.Metadata.Name))
			if !enrollment {
				current, err := provider.Get(suite.Ctx, suite.OrgID, *updated.Metadata.Name)
				Expect(err).NotTo(HaveOccurred())
				current.Status.Certificate = nil
				_, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, current)
				Expect(err).NotTo(HaveOccurred())
				resolved, err = findStoredCertificate(provider, suite.Ctx, suite.OrgID, retained)
				Expect(err).NotTo(HaveOccurred())
				Expect(resolved).To(BeNil())
			}
			_, err = provider.Delete(suite.Ctx, suite.OrgID, *updated.Metadata.Name)
			Expect(err).NotTo(HaveOccurred())
		}
	})
	It("When filtering a private certificate digest it should require internal selectors and isolate tenants", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		request := newStoredCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		created.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))
		created, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, created)
		Expect(err).NotTo(HaveOccurred())
		digest := sha256.Sum256(leaf.Raw)
		expression := "internal.certificateDigest=" + hex.EncodeToString(digest[:])
		_, err = provider.List(suite.Ctx, suite.OrgID, store.ListParams{FieldSelector: selector.NewFieldSelectorOrDie(expression)})
		Expect(err).To(HaveOccurred())
		filter := selector.NewFieldSelectorOrDie(expression, selector.WithPrivateSelectors())
		listed, err := provider.List(suite.Ctx, suite.OrgID, store.ListParams{FieldSelector: filter})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed.Items).To(HaveLen(1))
		Expect(listed.Items[0].Metadata.Name).To(Equal(created.Metadata.Name))
		foreign, err := provider.List(suite.Ctx, uuid.New(), store.ListParams{FieldSelector: filter})
		Expect(err).NotTo(HaveOccurred())
		Expect(foreign.Items).To(BeEmpty())
	})

})
