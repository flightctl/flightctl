package restore_test

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("EnrollmentRequest restore operations", func() {
	var s *RestoreTestSuite

	BeforeEach(func() {
		s = &RestoreTestSuite{}
		s.Setup()
	})

	AfterEach(func() {
		s.Teardown()
	})

	Context("PrepareEnrollmentRequestsAfterRestore", func() {
		It("should annotate non-approved enrollment requests with awaitingReconnect", func() {
			erStore := s.Store.EnrollmentRequest()

			nonApprovedName, nonApprovedCSR := GenerateDeviceNameAndCSR()
			toApproveName, toApproveCSR := GenerateDeviceNameAndCSR()
			alreadyAnnotatedName, alreadyAnnotatedCSR := GenerateDeviceNameAndCSR()

			nonApprovedER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr(nonApprovedName)},
				Spec:       api.EnrollmentRequestSpec{Csr: string(nonApprovedCSR)},
			}

			toApproveER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr(toApproveName)},
				Spec:       api.EnrollmentRequestSpec{Csr: string(toApproveCSR)},
			}

			alreadyAnnotatedER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(alreadyAnnotatedName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "true",
					},
				},
				Spec: api.EnrollmentRequestSpec{Csr: string(alreadyAnnotatedCSR)},
			}

			_, st := s.Handler.CreateEnrollmentRequest(s.Ctx, s.OrgID, nonApprovedER)
			Expect(st.Code).To(BeEquivalentTo(201))

			_, st = s.Handler.CreateEnrollmentRequest(s.Ctx, s.OrgID, toApproveER)
			Expect(st.Code).To(BeEquivalentTo(201))

			internalCtx := context.WithValue(s.Ctx, consts.InternalRequestCtxKey, true)
			_, st = s.Handler.CreateEnrollmentRequest(internalCtx, s.OrgID, alreadyAnnotatedER)
			Expect(st.Code).To(BeEquivalentTo(201))

			By("Debug: Verifying annotation was preserved")
			createdER, err := erStore.Get(s.Ctx, s.OrgID, *alreadyAnnotatedER.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			By(fmt.Sprintf("Debug: Created ER annotations: %+v", createdER.Metadata.Annotations))

			mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(s.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}

			_, st = s.Handler.ApproveEnrollmentRequest(ctxApproval, s.OrgID, toApproveName, approval)
			Expect(st.Code).To(BeEquivalentTo(200))

			By("Debug: Listing all enrollment requests before PrepareEnrollmentRequestsAfterRestore")
			allERs, err := erStore.List(s.Ctx, s.OrgID, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())

			for i, er := range allERs.Items {
				By(fmt.Sprintf("ER %d: Name=%s, Status=%+v, Annotations=%+v", i, *er.Metadata.Name, er.Status, er.Metadata.Annotations))
			}

			updatedCount, err := s.RestoreStore.PrepareEnrollmentRequestsAfterRestore(s.Ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedCount).To(Equal(int64(1)), "Should update only the non-approved enrollment request")

			updatedNonApproved, err := erStore.Get(s.Ctx, s.OrgID, nonApprovedName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedNonApproved.Metadata.Annotations).ToNot(BeNil())
			Expect(*updatedNonApproved.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))

			updatedApproved, err := erStore.Get(s.Ctx, s.OrgID, toApproveName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedApproved.Metadata.Annotations).ToNot(BeNil())
			Expect(*updatedApproved.Metadata.Annotations).To(BeEmpty())

			updatedAlreadyAnnotated, err := erStore.Get(s.Ctx, s.OrgID, alreadyAnnotatedName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedAlreadyAnnotated.Metadata.Annotations).ToNot(BeNil())
			Expect(*updatedAlreadyAnnotated.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
		})

		It("should handle enrollment requests with nil status", func() {
			erStore := s.Store.EnrollmentRequest()

			nilStatusName, nilStatusCSR := GenerateDeviceNameAndCSR()
			nilStatusER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr(nilStatusName)},
				Spec:       api.EnrollmentRequestSpec{Csr: string(nilStatusCSR)},
			}

			_, st := s.Handler.CreateEnrollmentRequest(s.Ctx, s.OrgID, nilStatusER)
			Expect(st.Code).To(BeEquivalentTo(201))

			updatedCount, err := s.RestoreStore.PrepareEnrollmentRequestsAfterRestore(s.Ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedCount).To(Equal(int64(1)), "Should update enrollment request with nil status")

			updated, err := erStore.Get(s.Ctx, s.OrgID, nilStatusName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated.Metadata.Annotations).ToNot(BeNil())
			Expect(*updated.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
		})

		It("should handle enrollment requests with nil approval", func() {
			erStore := s.Store.EnrollmentRequest()

			nilApprovalName, nilApprovalCSR := GenerateDeviceNameAndCSR()
			nilApprovalER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr(nilApprovalName)},
				Spec:       api.EnrollmentRequestSpec{Csr: string(nilApprovalCSR)},
			}

			_, st := s.Handler.CreateEnrollmentRequest(s.Ctx, s.OrgID, nilApprovalER)
			Expect(st.Code).To(BeEquivalentTo(201))

			updatedCount, err := s.RestoreStore.PrepareEnrollmentRequestsAfterRestore(s.Ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedCount).To(Equal(int64(1)), "Should update enrollment request with nil approval")

			updated, err := erStore.Get(s.Ctx, s.OrgID, nilApprovalName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated.Metadata.Annotations).ToNot(BeNil())
			Expect(*updated.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
		})

		It("should return zero when no enrollment requests need updating", func() {
			toApproveName, toApproveCSR := GenerateDeviceNameAndCSR()
			toApproveER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr(toApproveName)},
				Spec:       api.EnrollmentRequestSpec{Csr: string(toApproveCSR)},
			}

			_, st := s.Handler.CreateEnrollmentRequest(s.Ctx, s.OrgID, toApproveER)
			Expect(st.Code).To(BeEquivalentTo(201))

			mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(s.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}

			_, st = s.Handler.ApproveEnrollmentRequest(ctxApproval, s.OrgID, toApproveName, approval)
			Expect(st.Code).To(BeEquivalentTo(200))

			updatedCount, err := s.RestoreStore.PrepareEnrollmentRequestsAfterRestore(s.Ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedCount).To(Equal(int64(0)), "Should not update any approved enrollment requests")
		})
	})

	Context("PrepareEnrollmentRequestsAfterRestore integration", func() {
		It("should annotate enrollment requests and create devices with awaitingReconnect status", func() {
			er1 := CreateTestER()
			er1Name := lo.FromPtr(er1.Metadata.Name)

			er2 := CreateTestER()
			er2Name := lo.FromPtr(er2.Metadata.Name)

			er3 := CreateTestER()
			er3Name := lo.FromPtr(er3.Metadata.Name)

			By("creating enrollment requests")
			internalCtx := context.WithValue(s.Ctx, consts.InternalRequestCtxKey, true)
			_, status := s.Handler.CreateEnrollmentRequest(internalCtx, s.OrgID, er1)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			_, status = s.Handler.CreateEnrollmentRequest(internalCtx, s.OrgID, er2)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			_, status = s.Handler.CreateEnrollmentRequest(internalCtx, s.OrgID, er3)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			By("approving one enrollment request before restore")
			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(s.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}

			_, st := s.Handler.ApproveEnrollmentRequest(ctxApproval, s.OrgID, er1Name, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("simulating restore process - annotating non-approved enrollment requests")
			enrollmentRequestsUpdated, err := s.RestoreStore.PrepareEnrollmentRequestsAfterRestore(s.Ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(enrollmentRequestsUpdated).To(Equal(int64(2)), "Should update 2 non-approved enrollment requests")

			By("verifying enrollment requests were annotated")
			er2Updated, status := s.Handler.GetEnrollmentRequest(s.Ctx, s.OrgID, er2Name)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(er2Updated.Metadata.Annotations).ToNot(BeNil())
			Expect(*er2Updated.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))

			er3Updated, status := s.Handler.GetEnrollmentRequest(s.Ctx, s.OrgID, er3Name)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(er3Updated.Metadata.Annotations).ToNot(BeNil())
			Expect(*er3Updated.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))

			er1Updated, status := s.Handler.GetEnrollmentRequest(s.Ctx, s.OrgID, er1Name)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(er1Updated.Metadata.Annotations).ToNot(BeNil())
			Expect(*er1Updated.Metadata.Annotations).To(BeEmpty())

			By("approving the annotated enrollment requests and verifying device creation")
			_, st = s.Handler.ApproveEnrollmentRequest(ctxApproval, s.OrgID, er2Name, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			_, st = s.Handler.ApproveEnrollmentRequest(ctxApproval, s.OrgID, er3Name, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying devices were created with awaitingReconnect status")
			device2, status := s.Handler.GetDevice(s.Ctx, s.OrgID, er2Name)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device2).ToNot(BeNil())
			Expect(device2.Metadata.Annotations).ToNot(BeNil())
			Expect(*device2.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
			Expect(device2.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))

			device3, status := s.Handler.GetDevice(s.Ctx, s.OrgID, er3Name)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device3).ToNot(BeNil())
			Expect(device3.Metadata.Annotations).ToNot(BeNil())
			Expect(*device3.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
			Expect(device3.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
		})
	})
})
