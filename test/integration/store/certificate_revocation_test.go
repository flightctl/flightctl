package store_test

import (
	"context"
	"encoding/pem"
	"errors"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("CSR revocation mutations", func() {
	It("When revoking an issued CSR it should atomically return immutable state and preserve issuance", func() {
		suite := newCertificatePersistenceSuite()
		ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
		defer cancel()
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		request := newStoredCSR()
		created, err := provider.Create(ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())

		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		issuedAt := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
		issued := &domain.CertificateSigningRequest{
			Metadata: created.Metadata,
			Spec:     created.Spec,
			Status: &domain.CertificateSigningRequestStatus{
				Conditions: []domain.Condition{{
					Type:               domain.ConditionTypeCertificateSigningRequestApproved,
					Status:             domain.ConditionStatusTrue,
					Reason:             "Approved",
					LastTransitionTime: issuedAt,
				}},
				Certificate: lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})),
				IssuedAt:    lo.ToPtr(issuedAt),
			},
		}
		issued, err = seedCSRStatus(provider, ctx, suite.OrgID, issued)
		Expect(err).NotTo(HaveOccurred())

		revokedAt := time.Date(2025, 3, 5, 6, 7, 8, 0, time.UTC)
		revocation := domain.EnrollmentCertificateRevocation{
			Reason:    "Credential compromised",
			RevokedBy: "security-officer",
			RevokedAt: revokedAt,
		}
		type revokeResult struct {
			updated *domain.CertificateSigningRequest
			before  *domain.CertificateSigningRequest
			changed bool
			err     error
		}
		results := make(chan revokeResult, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				updated, before, changed, err := provider.Revoke(ctx, suite.OrgID, *issued.Metadata.Name, issued, revocation)
				results <- revokeResult{updated: updated, before: before, changed: changed, err: err}
			}()
		}
		wg.Wait()
		close(results)
		var revoked, before *domain.CertificateSigningRequest
		changedCount := 0
		for result := range results {
			Expect(result.err).NotTo(HaveOccurred())
			if result.changed {
				changedCount++
				revoked = result.updated
				before = result.before
				continue
			}
			Expect(result.updated.Status.Revocation).To(Equal(&revocation))
		}
		Expect(changedCount).To(Equal(1))
		Expect(before.Status.Revocation).To(BeNil())
		Expect(revoked.Status.Revocation).To(Equal(&revocation))
		Expect(revoked.Status.Certificate).To(Equal(issued.Status.Certificate))
		Expect(revoked.Status.IssuedAt).To(Equal(issued.Status.IssuedAt))
		Expect(revoked.Metadata.ResourceVersion).NotTo(Equal(issued.Metadata.ResourceVersion))

		current, err := provider.Get(ctx, suite.OrgID, *revoked.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Revocation).To(Equal(&revocation))
		repeated, _, changed, err := provider.Revoke(ctx, suite.OrgID, *current.Metadata.Name, current, domain.EnrollmentCertificateRevocation{
			Reason:    "Different reason",
			RevokedBy: "different-actor",
			RevokedAt: revokedAt.Add(time.Hour),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
		Expect(repeated.Status.Revocation).To(Equal(&revocation))
		Expect(repeated.Metadata.ResourceVersion).To(Equal(current.Metadata.ResourceVersion))
	})

	It("When a CSR is deleted and recreated under the same name it should reject a stale revoke", func() {
		suite := newCertificatePersistenceSuite()
		ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
		defer cancel()
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		request := newStoredCSR()
		created, err := provider.Create(ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())

		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		issuedAt := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
		issued := &domain.CertificateSigningRequest{
			Metadata: created.Metadata,
			Spec:     created.Spec,
			Status: &domain.CertificateSigningRequestStatus{
				Conditions: []domain.Condition{{
					Type:               domain.ConditionTypeCertificateSigningRequestApproved,
					Status:             domain.ConditionStatusTrue,
					Reason:             "Approved",
					LastTransitionTime: issuedAt,
				}},
				Certificate: lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})),
				IssuedAt:    lo.ToPtr(issuedAt),
			},
		}
		issued, err = seedCSRStatus(provider, ctx, suite.OrgID, issued)
		Expect(err).NotTo(HaveOccurred())

		deleted, err := provider.Delete(ctx, suite.OrgID, *issued.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())

		replacement := newStoredCSR()
		replacement.Metadata.Name = issued.Metadata.Name
		_, err = provider.Create(ctx, suite.OrgID, &replacement)
		Expect(err).NotTo(HaveOccurred())
		recreatedAt := issued.Metadata.CreationTimestamp.Add(time.Minute)
		Expect(suite.DB.Model(&model.CertificateSigningRequest{}).
			Where("org_id = ? AND name = ?", suite.OrgID, *issued.Metadata.Name).
			Update("created_at", recreatedAt).Error).To(Succeed())

		replacementIssued, err := provider.Get(ctx, suite.OrgID, *issued.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(replacementIssued.Metadata.CreationTimestamp).To(Equal(&recreatedAt))
		replacementIssued, err = seedCSRStatus(provider, ctx, suite.OrgID, replacementIssued)
		Expect(err).NotTo(HaveOccurred())

		revocation := domain.EnrollmentCertificateRevocation{
			Reason:    "Replacement compromised",
			RevokedBy: "security-officer",
			RevokedAt: time.Date(2025, 3, 5, 6, 7, 8, 0, time.UTC),
		}
		_, _, changed, err := provider.Revoke(ctx, suite.OrgID, *issued.Metadata.Name, replacementIssued, revocation)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())

		updated, before, changed, err := provider.Revoke(ctx, suite.OrgID, *issued.Metadata.Name, issued, domain.EnrollmentCertificateRevocation{
			Reason:    "Stale revocation",
			RevokedBy: "security-officer",
			RevokedAt: time.Date(2025, 3, 6, 7, 8, 9, 0, time.UTC),
		})
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, flterrors.ErrNoRowsUpdated)).To(BeTrue())
		Expect(changed).To(BeFalse())
		Expect(updated).To(BeNil())
		Expect(before).NotTo(BeNil())
		Expect(before.Metadata.CreationTimestamp).To(Equal(issued.Metadata.CreationTimestamp))

		current, err := provider.Get(ctx, suite.OrgID, *issued.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Revocation).To(Equal(&revocation))
	})
})
