package store_test

import (
	"context"
	"encoding/pem"
	"errors"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	"github.com/flightctl/flightctl/internal/store/model"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

func issueCSRWithCertificate(suite *certificatePersistenceSuite, ctx context.Context, provider csrstore.Store, created *domain.CertificateSigningRequest) *domain.CertificateSigningRequest {
	parsed, err := fcrypto.ParseCSR(created.Spec.Request)
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
	return issued
}

var _ = Describe("CSR revocation mutations", func() {
	It("When revoking an issued CSR it should atomically return immutable state and preserve issuance", func() {
		suite := newCertificatePersistenceSuite()
		ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
		defer cancel()
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		request := newStoredCSR()
		created, err := provider.Create(ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())

		issued := issueCSRWithCertificate(suite, ctx, provider, created)

		revokedAt := time.Date(2025, 3, 5, 6, 7, 8, 0, time.UTC)
		revocation := domain.EnrollmentCertificateRevocation{
			Reason:    "Credential compromised",
			RevokedBy: "security-officer",
			RevokedAt: revokedAt,
		}
		type revokeResult struct {
			updated *domain.CertificateSigningRequest
			changed bool
			err     error
		}
		results := make(chan revokeResult, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				updated, changed, err := provider.Revoke(ctx, suite.OrgID, *issued.Metadata.Name, revocation, nil)
				results <- revokeResult{updated: updated, changed: changed, err: err}
			}()
		}
		wg.Wait()
		close(results)
		var revoked *domain.CertificateSigningRequest
		changedCount := 0
		for result := range results {
			Expect(result.err).NotTo(HaveOccurred())
			if result.changed {
				changedCount++
				revoked = result.updated
				continue
			}
			Expect(result.updated.Status.Revocation).To(Equal(&revocation))
		}
		Expect(changedCount).To(Equal(1))
		Expect(revoked.Status.Revocation).To(Equal(&revocation))
		Expect(revoked.Status.Certificate).To(Equal(issued.Status.Certificate))
		Expect(revoked.Status.IssuedAt).To(Equal(issued.Status.IssuedAt))
		Expect(revoked.Metadata.ResourceVersion).NotTo(Equal(issued.Metadata.ResourceVersion))

		current, err := provider.Get(ctx, suite.OrgID, *revoked.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Revocation).To(Equal(&revocation))
		repeated, changed, err := provider.Revoke(ctx, suite.OrgID, *current.Metadata.Name, domain.EnrollmentCertificateRevocation{
			Reason:    "Different reason",
			RevokedBy: "different-actor",
			RevokedAt: revokedAt.Add(time.Hour),
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
		Expect(repeated.Status.Revocation).To(Equal(&revocation))
		Expect(repeated.Metadata.ResourceVersion).To(Equal(current.Metadata.ResourceVersion))
	})

	It("When a CSR is deleted and recreated under the same name it should revoke the current row", func() {
		suite := newCertificatePersistenceSuite()
		ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
		defer cancel()
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		request := newStoredCSR()
		created, err := provider.Create(ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())

		issued := issueCSRWithCertificate(suite, ctx, provider, created)

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
		revoked, changed, err := provider.Revoke(ctx, suite.OrgID, *issued.Metadata.Name, revocation, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(revoked.Metadata.CreationTimestamp).To(Equal(&recreatedAt))
		Expect(revoked.Status.Certificate).To(Equal(replacementIssued.Status.Certificate))

		current, err := provider.Get(ctx, suite.OrgID, *issued.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Revocation).To(Equal(&revocation))
	})

	It("When a revocation transaction rolls back it should leave no revocation or event", func() {
		suite := newCertificatePersistenceSuite()
		ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
		defer cancel()
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		events := eventstore.NewEventStore(suite.DB, suite.Log)
		request := newStoredCSR()
		created, err := provider.Create(ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		revocation := domain.EnrollmentCertificateRevocation{Reason: "Credential compromised", RevokedBy: "security-officer", RevokedAt: time.Now().UTC()}

		err = provider.WithTransaction(ctx, func(txCtx context.Context) error {
			_, _, revokeErr := provider.Revoke(txCtx, suite.OrgID, *created.Metadata.Name, revocation, nil)
			if revokeErr != nil {
				return revokeErr
			}
			if eventErr := events.Create(txCtx, suite.OrgID, domain.GetBaseEvent(txCtx, domain.CertificateSigningRequestKind, *created.Metadata.Name, domain.EventReasonEnrollmentCredentialRevoked, "Enrollment credential revoked.", nil)); eventErr != nil {
				return eventErr
			}
			return errors.New("rollback")
		})
		Expect(err).To(MatchError("rollback"))

		current, err := provider.Get(ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Revocation).To(BeNil())
		eventList, err := events.List(ctx, suite.OrgID, store.ListParams{})
		Expect(err).NotTo(HaveOccurred())
		for _, event := range eventList.Items {
			Expect(event.Reason != domain.EventReasonEnrollmentCredentialRevoked || event.InvolvedObject.Name != *created.Metadata.Name).To(BeTrue(), "rolled-back revocation event must not persist")
		}
	})

	It("When revoking a legacy-signer CSR it should retain the certificate digest", func() {
		suite := newCertificatePersistenceSuite()
		ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
		defer cancel()
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		request := newStoredCSR()
		request.Spec.SignerName = suite.cfg.CA.DeviceEnrollmentSignerName
		created, err := provider.Create(ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		issued := issueCSRWithCertificate(suite, ctx, provider, created)

		legacy, _, _, err := provider.Mutate(ctx, suite.OrgID, *issued.Metadata.Name, issued, func(mutation *csrstore.CertificateSigningRequestMutation) error {
			mutation.Resource().Spec.SignerName = "legacy.example/changed"
			return nil
		})
		Expect(err).NotTo(HaveOccurred())

		var stored model.CertificateSigningRequest
		Expect(suite.DB.Where("org_id = ? AND name = ?", suite.OrgID, *legacy.Metadata.Name).Take(&stored).Error).To(Succeed())
		Expect(stored.CertificateDigest).NotTo(BeNil())
		digest := *stored.CertificateDigest

		revoked, changed, err := provider.Revoke(ctx, suite.OrgID, *legacy.Metadata.Name, domain.EnrollmentCertificateRevocation{
			Reason:    "Credential compromised",
			RevokedBy: "security-officer",
			RevokedAt: time.Now().UTC(),
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(revoked.Status.Certificate).To(Equal(issued.Status.Certificate))

		var after model.CertificateSigningRequest
		Expect(suite.DB.Where("org_id = ? AND name = ?", suite.OrgID, *legacy.Metadata.Name).Take(&after).Error).To(Succeed())
		Expect(after.CertificateDigest).NotTo(BeNil())
		Expect(*after.CertificateDigest).To(Equal(digest))
	})
})
