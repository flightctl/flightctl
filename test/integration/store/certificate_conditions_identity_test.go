package store_test

import (
	"context"
	"encoding/pem"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

var _ = Describe("Condition updates across CSR name reuse", func() {
	It("When an old condition write races recreation it should reject the stale attempt and require a fresh condition update", func() {
		suite := newCertificatePersistenceSuite()
		ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
		defer cancel()
		createdAt := time.Date(2025, 3, 4, 5, 6, 7, 123456000, time.UTC)
		recreatedAt := time.Date(2025, 3, 4, 5, 6, 7, 123457000, time.UTC)
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB.Session(&gorm.Session{NowFunc: func() time.Time { return createdAt }}), suite.Log)
		request := newStoredCSR()
		request.Spec.SignerName = "example.org/enrollment"
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		oldLeaf, err := suite.caClient.IssueRequestedClientCertificate(ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		newLeaf, err := suite.caClient.IssueRequestedClientCertificate(ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		Expect(newLeaf.Raw).NotTo(Equal(oldLeaf.Raw))
		original, err := provider.Create(ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		original.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: oldLeaf.Raw}))
		original, err = seedCSRStatus(provider, ctx, suite.OrgID, original)
		Expect(err).NotTo(HaveOccurred())

		release := make(chan struct{})
		ready := make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		var pause sync.Once
		result := make(chan error, 1)
		go func() {
			_, _, _, err := provider.Mutate(ctx, suite.OrgID, *original.Metadata.Name, nil, func(mutation *store.Mutation[domain.CertificateSigningRequest]) error {
				if err := mutation.RequireExisting(); err != nil {
					return err
				}
				domain.SetStatusCondition(&mutation.Resource().Status.Conditions, domain.Condition{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"})
				pause.Do(func() {
					close(ready)
					select {
					case <-release:
					case <-ctx.Done():
					}
				})
				return ctx.Err()
			})
			result <- err
		}()

		select {
		case <-ready:
		case <-ctx.Done():
			Fail("condition write did not reach the database before its deadline")
		}
		_, err = provider.Delete(ctx, suite.OrgID, *original.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		replacementProvider := csrstore.NewCertificateSigningRequestStore(suite.DB.Session(&gorm.Session{NowFunc: func() time.Time { return recreatedAt }}), suite.Log)
		replacement, err := replacementProvider.Create(ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		replacement.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: newLeaf.Raw}))
		replacement, err = seedCSRStatus(replacementProvider, ctx, suite.OrgID, replacement)
		Expect(err).NotTo(HaveOccurred())
		Expect(replacement.Metadata.ResourceVersion).To(Equal(original.Metadata.ResourceVersion))
		close(release)
		released = true
		select {
		case err := <-result:
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		case <-ctx.Done():
			Fail("condition update did not finish before its deadline")
		}
		current, err := provider.Get(ctx, suite.OrgID, *replacement.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Certificate).To(Equal(replacement.Status.Certificate))
		Expect(current.Status.Conditions).To(Equal(replacement.Status.Conditions))
		Expect(provider.UpdateConditions(ctx, suite.OrgID, *replacement.Metadata.Name, []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}})).To(Succeed())
		current, err = provider.Get(ctx, suite.OrgID, *replacement.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Certificate).To(Equal(replacement.Status.Certificate))
		Expect(current.Status.Conditions).To(ContainElement(HaveField("Reason", "Denied")))
	})
})
