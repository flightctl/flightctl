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
				created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
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

				_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
				Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

				By("verifying device creation with expected annotations and status")
				device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
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
			created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request with additional labels")
			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true", "environment": "test"},
			}

			_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device was created with awaitingReconnect annotation and approval labels")
			device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
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
			created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
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

			_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device was created with awaitingReconnect annotation and status")
			device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device).ToNot(BeNil())
			Expect(device.Metadata.Annotations).ToNot(BeNil())
			Expect(*device.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
			Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			Expect(device.Status.Summary.Info).ToNot(BeNil())
			Expect(*device.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))
		})
	})

	Context("createDeviceFromEnrollmentRequest with enrollment systemInfo", func() {
		It("When enrollment systemInfo has distroId it should copy it onto the device after approval", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			enrollmentStatus := api.NewDeviceStatus()
			enrollmentStatus.SystemInfo.OperatingSystem = "linux"
			enrollmentStatus.SystemInfo.Set("distroId", "rhel")
			enrollmentStatus.SystemInfo.Set("distroVersion", "9.5 (Plow)")
			er.Spec.DeviceStatus = &enrollmentStatus

			By("creating enrollment request with systemInfo")
			created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request")
			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}
			_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device systemInfo")
			device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device.Status).ToNot(BeNil())
			id, found := device.Status.SystemInfo.Get("distroId")
			Expect(found).To(BeTrue())
			Expect(id).To(Equal("rhel"))
			version, found := device.Status.SystemInfo.Get("distroVersion")
			Expect(found).To(BeTrue())
			Expect(version).To(Equal("9.5 (Plow)"))
			Expect(device.Status.SystemInfo.OperatingSystem).To(Equal("linux"))
			Expect(device.Status.Lifecycle.Status).To(Equal(api.DeviceLifecycleStatusEnrolled))
		})

		It("When awaitingReconnect is set it should keep systemInfo and set awaiting reconnect summary", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Metadata.Annotations = &map[string]string{
				api.DeviceAnnotationAwaitingReconnect: "true",
			}
			enrollmentStatus := api.NewDeviceStatus()
			enrollmentStatus.SystemInfo.Set("distroId", "rhel")
			enrollmentStatus.SystemInfo.Set("distroVersion", "10.0 (Coughlan)")
			er.Spec.DeviceStatus = &enrollmentStatus

			By("creating enrollment request")
			created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
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
			_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device systemInfo and awaiting reconnect summary")
			device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			id, found := device.Status.SystemInfo.Get("distroId")
			Expect(found).To(BeTrue())
			Expect(id).To(Equal("rhel"))
			version, found := device.Status.SystemInfo.Get("distroVersion")
			Expect(found).To(BeTrue())
			Expect(version).To(Equal("10.0 (Coughlan)"))
		})
	})

	Context("createDeviceFromEnrollmentRequest with osMode capabilities", func() {
		It("When osMode is package it should set device capabilities.osMode to package", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Spec.Capabilities = &api.DeviceCapabilities{OsMode: lo.ToPtr(api.OsModePackage)}

			By("creating enrollment request with osMode=package")
			created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request")
			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}
			_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device capabilities")
			device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device.Status.Capabilities).ToNot(BeNil())
			Expect(device.Status.Capabilities.OsMode).ToNot(BeNil())
			Expect(*device.Status.Capabilities.OsMode).To(Equal(api.OsModePackage))
			Expect(device.Status.Capabilities.DeltaEligible).To(BeNil())
		})

		It("When deltaEligible is false it should copy false onto the device", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Spec.Capabilities = &api.DeviceCapabilities{OsMode: lo.ToPtr(api.OsModeImage), DeltaEligible: lo.ToPtr(false)}

			By("creating enrollment request with deltaEligible=false")
			created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request")
			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}
			_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device capabilities")
			device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device.Status.Capabilities).ToNot(BeNil())
			Expect(device.Status.Capabilities.OsMode).ToNot(BeNil())
			Expect(*device.Status.Capabilities.OsMode).To(Equal(api.OsModeImage))
			Expect(device.Status.Capabilities.DeltaEligible).ToNot(BeNil())
			Expect(*device.Status.Capabilities.DeltaEligible).To(BeFalse())
		})

		It("When osMode is image it should set device capabilities.osMode to image", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Spec.Capabilities = &api.DeviceCapabilities{OsMode: lo.ToPtr(api.OsModeImage), DeltaEligible: lo.ToPtr(true)}

			By("creating enrollment request with osMode=image")
			created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request")
			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}
			_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device capabilities")
			device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device.Status.Capabilities).ToNot(BeNil())
			Expect(device.Status.Capabilities.OsMode).ToNot(BeNil())
			Expect(*device.Status.Capabilities.OsMode).To(Equal(api.OsModeImage))
			Expect(device.Status.Capabilities.DeltaEligible).ToNot(BeNil())
			Expect(*device.Status.Capabilities.DeltaEligible).To(BeTrue())
		})

		It("When osMode is absent it should leave device capabilities nil", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			By("creating enrollment request without osMode")
			created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request")
			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}
			_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device capabilities are nil")
			device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device.Status.Capabilities).To(BeNil())
		})

		It("When only top-level spec.osMode is set it should copy it onto device capabilities.osMode", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Spec.OsMode = lo.ToPtr(api.OsModePackage)

			By("creating enrollment request with top-level osMode=package")
			created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("approving the enrollment request")
			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}
			_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying device capabilities")
			device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device.Status.Capabilities).ToNot(BeNil())
			Expect(device.Status.Capabilities.OsMode).ToNot(BeNil())
			Expect(*device.Status.Capabilities.OsMode).To(Equal(api.OsModePackage))
			Expect(device.Status.Capabilities.DeltaEligible).To(BeNil())
		})
	})

	Context("createDeviceFromEnrollmentRequest with replaceLabels", func() {
		DescribeTable("should handle replaceLabels correctly",
			func(
				agentLabels map[string]string,
				approvalLabels map[string]string,
				replaceLabels *bool,
				expectedLabels map[string]string,
			) {
				// Create enrollment request with agent-provided labels
				er := CreateTestER()
				erName := lo.FromPtr(er.Metadata.Name)
				er.Spec.Labels = &agentLabels

				By("creating enrollment request")
				created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, er)
				Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
				Expect(created).ToNot(BeNil())

				By("approving the enrollment request with specified replaceLabels setting")
				defaultOrg := &model.Organization{
					ID:          org.DefaultID,
					ExternalID:  org.DefaultID.String(),
					DisplayName: org.DefaultID.String(),
				}
				mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, map[string][]string{}, false, nil)
				ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

				approval := api.EnrollmentRequestApproval{
					Approved:      true,
					Labels:        &approvalLabels,
					ReplaceLabels: replaceLabels,
				}

				_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
				Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

				By("verifying device was created with expected labels")
				device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, erName)
				Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
				Expect(device).ToNot(BeNil())
				Expect(device.Metadata.Labels).ToNot(BeNil())
				Expect(*device.Metadata.Labels).To(Equal(expectedLabels))
			},
			Entry("When replaceLabels is nil it should merge labels (default)",
				map[string]string{"agent": "value1", "other": "value2"},
				map[string]string{"approval": "value3"},
				nil,
				map[string]string{"agent": "value1", "other": "value2", "approval": "value3"},
			),
			Entry("When replaceLabels is false it should merge labels",
				map[string]string{"agent": "value1", "other": "value2"},
				map[string]string{"approval": "value3"},
				lo.ToPtr(false),
				map[string]string{"agent": "value1", "other": "value2", "approval": "value3"},
			),
			Entry("When replaceLabels is true it should use only approval labels",
				map[string]string{"agent": "value1", "other": "value2"},
				map[string]string{"approval": "value3"},
				lo.ToPtr(true),
				map[string]string{"approval": "value3"},
			),
			Entry("When replaceLabels is true with empty approval labels it should have no labels",
				map[string]string{"agent": "value1", "other": "value2"},
				map[string]string{},
				lo.ToPtr(true),
				map[string]string{},
			),
			Entry("When replaceLabels is true with multiple approval labels it should only have those",
				map[string]string{"agent": "value1", "other": "value2", "another": "value3"},
				map[string]string{"env": "prod", "tier": "web"},
				lo.ToPtr(true),
				map[string]string{"env": "prod", "tier": "web"},
			),
			Entry("When replaceLabels is false and keys overlap it should give approval precedence",
				map[string]string{"shared": "agent-value", "agent": "value1"},
				map[string]string{"shared": "approval-value", "approval": "value3"},
				lo.ToPtr(false),
				map[string]string{"shared": "approval-value", "agent": "value1", "approval": "value3"},
			),
		)
	})
})
