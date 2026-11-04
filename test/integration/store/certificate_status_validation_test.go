package store_test

import (
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CSR status mutation validation", func() {
	DescribeTable("When an existing CSR mutation has missing state it should reject it without changing persisted status",
		func(clearResource bool) {
			suite := newCertificatePersistenceSuite()
			provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
			request := newStoredCSR()
			request.Status = &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{{
				Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue,
				Reason: "ExistingDenial", Message: "Original decision",
				LastTransitionTime: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
			}}}
			created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
			Expect(err).NotTo(HaveOccurred())
			before, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(before.Status.Conditions).To(Equal([]domain.Condition{{
				Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue,
				Reason: "ExistingDenial", Message: "Original decision",
				LastTransitionTime: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
			}}))

			_, _, _, err = provider.Mutate(suite.Ctx, suite.OrgID, *created.Metadata.Name, nil, func(mutation *csrstore.CertificateSigningRequestMutation) error {
				if err := mutation.RequireExisting(); err != nil {
					return err
				}
				if clearResource {
					mutation.CertificateSigningRequest = nil
				} else {
					mutation.CertificateSigningRequest.Status = nil
				}
				return nil
			})
			Expect(err).To(MatchError(flterrors.ErrResourceIsNil))
			after, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(after.Status.Conditions).To(Equal([]domain.Condition{{
				Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue,
				Reason: "ExistingDenial", Message: "Original decision",
				LastTransitionTime: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
			}}))
			Expect(after.Metadata.ResourceVersion).To(Equal(before.Metadata.ResourceVersion))
			Expect(after.Metadata.CreationTimestamp).To(Equal(before.Metadata.CreationTimestamp))
		},
		Entry("When status is nil it should retain the existing decision", false),
		Entry("When the resource is nil it should retain the existing decision", true),
	)

	It("When status is valid it should persist and return the new decision", func() {
		suite := newCertificatePersistenceSuite()
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		request := newStoredCSR()
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		updated, _, _, err := provider.Mutate(suite.Ctx, suite.OrgID, *created.Metadata.Name, nil, func(mutation *csrstore.CertificateSigningRequestMutation) error {
			if err := mutation.RequireExisting(); err != nil {
				return err
			}
			mutation.CertificateSigningRequest.Status = &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{{
				Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue,
				Reason: "NewDenial", Message: "New decision",
				LastTransitionTime: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
			}}}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		current, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Conditions).To(Equal([]domain.Condition{{
			Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue,
			Reason: "NewDenial", Message: "New decision",
			LastTransitionTime: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
		}}))
		Expect(updated.Status).To(Equal(current.Status))
		Expect(updated.Metadata.ResourceVersion).To(Equal(current.Metadata.ResourceVersion))
		Expect(current.Metadata.ResourceVersion).NotTo(Equal(created.Metadata.ResourceVersion))
	})

	It("When non-status fields are mutated it should persist them", func() {
		suite := newCertificatePersistenceSuite()
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		request := newStoredCSR()
		request.Metadata.Labels = &map[string]string{"original": "label"}
		request.Metadata.Annotations = &map[string]string{"original": "annotation"}
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())

		updated, _, _, err := provider.Mutate(suite.Ctx, suite.OrgID, *created.Metadata.Name, nil, func(mutation *csrstore.CertificateSigningRequestMutation) error {
			if err := mutation.RequireExisting(); err != nil {
				return err
			}
			current := mutation.CertificateSigningRequest
			current.Spec.Request = []byte("updated request")
			current.Metadata.Labels = &map[string]string{"updated": "label"}
			current.Metadata.Annotations = &map[string]string{"updated": "annotation"}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())

		current, err := provider.Get(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Spec.Request).To(Equal([]byte("updated request")))
		Expect(current.Metadata.Labels).To(Equal(&map[string]string{"updated": "label"}))
		Expect(current.Metadata.Annotations).To(Equal(&map[string]string{"updated": "annotation"}))
		Expect(updated.Spec).To(Equal(current.Spec))
		Expect(updated.Metadata.ResourceVersion).To(Equal(current.Metadata.ResourceVersion))
		Expect(current.Metadata.ResourceVersion).NotTo(Equal(created.Metadata.ResourceVersion))
	})
})
