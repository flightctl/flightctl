package service_test

import (
	"context"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/onsi/gomega/types"
	"github.com/samber/lo"
)

var _ = Describe("EnrollmentRequest Device Creation Unit Tests", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	Context("createDeviceFromEnrollmentRequest with awaitingReconnect annotation", func() {
		DescribeTable("should handle awaitingReconnect annotation transfer correctly",
			func(
				enrollmentRequestAnnotations *map[string]string,
				expectedDeviceAnnotations types.GomegaMatcher,
				expectedDeviceStatus types.GomegaMatcher,
			) {
				// Create enrollment request with specified annotations
				er := CreateTestER()
				erName := lo.FromPtr(er.Metadata.Name)
				er.Metadata.Annotations = enrollmentRequestAnnotations

				By("creating enrollment request")
				// Use internal request context to preserve annotations
				internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
				created, status := suite.Handler.CreateEnrollmentRequest(internalCtx, er)
				Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
				Expect(created).ToNot(BeNil())

				By("approving the enrollment request")
				defaultOrg := &model.Organization{
					ID:          org.DefaultID,
					ExternalID:  org.DefaultID.String(),
					DisplayName: org.DefaultID.String(),
				}
				mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, []string{}, nil)
				ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

				approval := api.EnrollmentRequestApproval{
					Approved: true,
					Labels:   &map[string]string{"approved": "true"},
				}

				_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, erName, approval)
				Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

				By("verifying device creation with expected annotations and status")
				device, status := suite.Handler.GetDevice(suite.Ctx, erName)
				Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
				Expect(device).ToNot(BeNil())
				Expect(device.Metadata.Annotations).To(expectedDeviceAnnotations)
				Expect(device.Status.Summary).To(expectedDeviceStatus)
			},
			Entry("should transfer awaitingReconnect annotation and set status",
				&map[string]string{
					api.DeviceAnnotationAwaitingReconnect: "true",
				},
				PointTo(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true")),
				And(
					HaveField("Status", Equal(api.DeviceSummaryStatusAwaitingReconnect)),
					HaveField("Info", PointTo(Equal("Device has not reconnected since restore to confirm its current state."))),
				),
			),
			Entry("should not transfer awaitingReconnect annotation when not present",
				nil,
				Not(BeNil()),
				Not(HaveField("Status", Equal(api.DeviceSummaryStatusAwaitingReconnect))),
			),
			Entry("should not transfer awaitingReconnect annotation when false",
				&map[string]string{
					api.DeviceAnnotationAwaitingReconnect: "false",
				},
				Not(BeNil()),
				Not(HaveField("Status", Equal(api.DeviceSummaryStatusAwaitingReconnect))),
			),
			Entry("should not transfer awaitingReconnect annotation when empty",
				&map[string]string{
					api.DeviceAnnotationAwaitingReconnect: "",
				},
				Not(BeNil()),
				Not(HaveField("Status", Equal(api.DeviceSummaryStatusAwaitingReconnect))),
			),
		)

		It("should preserve existing device annotations when adding awaitingReconnect", func() {
			// Create enrollment request with awaitingReconnect annotation
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Metadata.Annotations = &map[string]string{
				api.DeviceAnnotationAwaitingReconnect: "true",
			}

			By("creating enrollment request")
			// Use internal request context to preserve annotations
			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			created, status := suite.Handler.CreateEnrollmentRequest(internalCtx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request with additional labels")
			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, []string{}, nil)
			ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true", "environment": "test"},
			}

			_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device was created with awaitingReconnect annotation and approval labels")
			device, status := suite.Handler.GetDevice(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device).ToNot(BeNil())
			Expect(device.Metadata.Annotations).ToNot(BeNil())
			Expect(*device.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
			Expect(device.Metadata.Labels).ToNot(BeNil())
			Expect(*device.Metadata.Labels).To(HaveKeyWithValue("approved", "true"))
			Expect(*device.Metadata.Labels).To(HaveKeyWithValue("environment", "test"))
			Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			Expect(device.Status.Summary.Info).ToNot(BeNil())
			Expect(*device.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))
		})

		It("should handle enrollment request with awaitingReconnect annotation but nil status", func() {
			// Create enrollment request with awaitingReconnect annotation but nil status
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Metadata.Annotations = &map[string]string{
				api.DeviceAnnotationAwaitingReconnect: "true",
			}
			er.Status = nil

			By("creating enrollment request with nil status")
			// Use internal request context to preserve annotations
			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			created, status := suite.Handler.CreateEnrollmentRequest(internalCtx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request")
			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, []string{}, nil)
			ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}

			_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device was created with awaitingReconnect annotation and status")
			device, status := suite.Handler.GetDevice(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device).ToNot(BeNil())
			Expect(device.Metadata.Annotations).ToNot(BeNil())
			Expect(*device.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
			Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			Expect(device.Status.Summary.Info).ToNot(BeNil())
			Expect(*device.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))
		})
	})
})
