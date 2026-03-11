package service_test

import (
	"context"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("EnrollmentRequest AwaitingReconnect Integration Tests", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	Context("Device creation from enrollment request with awaitingReconnect annotation", func() {
		It("should transfer awaitingReconnect annotation and status to device", func() {
			// Create enrollment request with awaitingReconnect annotation
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			// Add awaitingReconnect annotation to the enrollment request
			er.Metadata.Annotations = &map[string]string{
				api.DeviceAnnotationAwaitingReconnect: "true",
			}

			By("creating enrollment request with awaitingReconnect annotation")
			// Use internal request context to preserve annotations
			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			created, status := suite.Handler.CreateEnrollmentRequest(internalCtx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())
			Expect(created.Metadata.Annotations).ToNot(BeNil())
			Expect(*created.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))

			By("approving the enrollment request")
			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}

			_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device was created with awaitingReconnect annotation and status")
			device, status := suite.Handler.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device).ToNot(BeNil())
			Expect(device.Metadata.Annotations).ToNot(BeNil())
			Expect(*device.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
			Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			Expect(device.Status.Summary.Info).ToNot(BeNil())
			Expect(*device.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))
		})

		It("should not transfer awaitingReconnect annotation when not present", func() {
			// Create enrollment request without awaitingReconnect annotation
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			By("creating enrollment request without awaitingReconnect annotation")
			// Use internal request context to preserve annotations
			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			created, status := suite.Handler.CreateEnrollmentRequest(internalCtx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request")
			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}

			_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device was created without awaitingReconnect annotation and status")
			device, status := suite.Handler.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device).ToNot(BeNil())
			Expect(device.Metadata.Annotations).ToNot(BeNil())
			Expect(*device.Metadata.Annotations).To(BeEmpty())
			Expect(device.Status.Summary.Status).ToNot(Equal(api.DeviceSummaryStatusAwaitingReconnect))
		})

		It("should handle enrollment request with awaitingReconnect annotation but false value", func() {
			// Create enrollment request with awaitingReconnect annotation set to false
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			// Add awaitingReconnect annotation with false value
			er.Metadata.Annotations = &map[string]string{
				api.DeviceAnnotationAwaitingReconnect: "false",
			}

			By("creating enrollment request with awaitingReconnect annotation set to false")
			// Use internal request context to preserve annotations
			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			created, status := suite.Handler.CreateEnrollmentRequest(internalCtx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request")
			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}

			_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device was created without awaitingReconnect annotation and status")
			device, status := suite.Handler.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device).ToNot(BeNil())
			Expect(device.Metadata.Annotations).ToNot(BeNil())
			Expect(*device.Metadata.Annotations).To(BeEmpty())
			Expect(device.Status.Summary.Status).ToNot(Equal(api.DeviceSummaryStatusAwaitingReconnect))
		})

		It("should preserve existing device annotations when adding awaitingReconnect", func() {
			// Create enrollment request with awaitingReconnect annotation and other annotations
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			// Add awaitingReconnect annotation and other annotations
			er.Metadata.Annotations = &map[string]string{
				api.DeviceAnnotationAwaitingReconnect: "true",
				"custom-annotation":                   "custom-value",
				"another-annotation":                  "another-value",
			}

			By("creating enrollment request with awaitingReconnect and other annotations")
			// Use internal request context to preserve annotations
			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			created, status := suite.Handler.CreateEnrollmentRequest(internalCtx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request")
			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}

			_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device was created with awaitingReconnect annotation and status, but without other annotations")
			device, status := suite.Handler.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device).ToNot(BeNil())
			Expect(device.Metadata.Annotations).ToNot(BeNil())
			Expect(*device.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
			Expect(*device.Metadata.Annotations).ToNot(HaveKey("custom-annotation"))
			Expect(*device.Metadata.Annotations).ToNot(HaveKey("another-annotation"))
			Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			Expect(device.Status.Summary.Info).ToNot(BeNil())
			Expect(*device.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))
		})
	})

	// PrepareDevicesAfterRestore integration tests have been moved to test/integration/restore/enrollmentrequest_test.go
})
