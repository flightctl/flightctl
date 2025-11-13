package service_test

import (
	"context"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/onsi/gomega/types"
	"github.com/samber/lo"
)

var _ = Describe("EnrollmentRequest Integration Tests", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	// PATCH /api/v1/enrollmentrequests/{name}
	Context("Patch ER operations", func() {
		DescribeTable("should handle patch operations correctly", func(patch api.PatchRequest, patchedMatcher types.GomegaMatcher, statusMatcher types.GomegaMatcher) {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			By("creating initial EnrollmentRequest")
			created, status := suite.Handler.CreateEnrollmentRequest(suite.Ctx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("applying patch")
			patched, status := suite.Handler.PatchEnrollmentRequest(suite.Ctx, erName, patch)
			Expect(status.Code).To(statusMatcher)

			if IsStatusSuccessful(&status) {
				Expect(patched).ToNot(BeNil())
				// Verify expected changes
				if patchedMatcher != nil {
					Expect(patched).To(patchedMatcher)
				}
				// Verify status immutability
				VerifyERStatusUnchanged(patched, created)
			}

			By("verifying persistence after read back")
			retrieved, status := suite.Handler.GetEnrollmentRequest(suite.Ctx, erName)
			VerifyERStatusUnchanged(retrieved, created)
			if patchedMatcher != nil {
				Expect(retrieved).To(patchedMatcher, "should match expected patch result after read back")
			}
		},
			Entry("metadata label patch",
				NewLabelPatch("environment", "staging"),
				And(
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("environment", "staging"))),
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("test", "integration"))),
				),
				BeEquivalentTo(http.StatusOK),
			),

			Entry("multiple metadata operations",
				NewMultiLabelPatch(
					map[string]string{"environment": "production"},
					map[string]string{"test": "updated"},
				),
				And(
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("environment", "production"))),
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("test", "updated"))),
				),
				BeEquivalentTo(http.StatusOK),
			),

			Entry("patch with multiple label operations",
				NewMultiLabelPatch(
					map[string]string{"environment": "production", "version": "v2"},
					map[string]string{"test": "updated"},
				),
				And(
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("environment", "production"))),
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("version", "v2"))),
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("test", "updated"))),
				),
				BeEquivalentTo(http.StatusOK),
			),

			Entry("patch with status should ignore status",
				api.PatchRequest{{
					Op:    "add",
					Path:  "/metadata/labels/foo",
					Value: AnyPtr("bar"),
				}, {
					Op:   "add",
					Path: "/status/conditions",
					Value: AnyPtr([]api.Condition{{
						Type:    api.ConditionTypeEnrollmentRequestApproved,
						Status:  api.ConditionStatusTrue,
						Reason:  "FakeApproval",
						Message: "This should be ignored",
					}}),
				}},
				nil, // Don't care about result on failure
				BeEquivalentTo(http.StatusBadRequest),
			),

			Entry("patch with spec modifications should fail",
				api.PatchRequest{{
					Op:    "replace",
					Path:  "/spec/csr",
					Value: AnyPtr("fake-csr-data"),
				}},
				HaveField("Spec.Csr", Not(Equal("fake-csr-data"))),
				BeEquivalentTo(http.StatusBadRequest),
			),
		)
	})

	// PUT /api/v1/enrollmentrequests/{name}
	Context("Replace ER operations", func() {
		DescribeTable("should handle replace operations correctly", func(replaceFunc func(api.EnrollmentRequest) api.EnrollmentRequest, replacedMatcher types.GomegaMatcher, statusMatcher types.GomegaMatcher) {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			By("creating initial EnrollmentRequest")
			created, status := suite.Handler.CreateEnrollmentRequest(suite.Ctx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("preparing replacement")
			replacement := replaceFunc(*created)

			By("applying replacement")
			replaced, status := suite.Handler.ReplaceEnrollmentRequest(suite.Ctx, erName, replacement)
			Expect(status.Code).To(statusMatcher)

			if IsStatusSuccessful(&status) {
				Expect(replaced).ToNot(BeNil())
				// Verify status immutability
				VerifyERStatusUnchanged(replaced, created)
				// Verify expected changes
				if replacedMatcher != nil {
					Expect(replaced).To(replacedMatcher)
				}
			}

			By("verifying persistence after read back")
			final, status := suite.Handler.GetEnrollmentRequest(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			VerifyERStatusUnchanged(final, created)
			if replacedMatcher != nil {
				Expect(final).To(replacedMatcher, "should match expected replace result after read back")
			}
		},
			Entry("normal replace with labels",
				func(er api.EnrollmentRequest) api.EnrollmentRequest {
					er.Metadata.Labels = &map[string]string{
						"test":        "integration",
						"environment": "production",
						"version":     "v2",
					}
					return er
				},
				And(
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("environment", "production"))),
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("version", "v2"))),
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("test", "integration"))),
				),
				BeEquivalentTo(http.StatusOK),
			),

			Entry("replace with status should ignore status",
				func(er api.EnrollmentRequest) api.EnrollmentRequest {
					er.Metadata.Labels = &map[string]string{
						"foo": "bar",
					}
					er.Status = &api.EnrollmentRequestStatus{
						Conditions: []api.Condition{
							{
								Type:    api.ConditionTypeEnrollmentRequestApproved,
								Status:  api.ConditionStatusTrue,
								Reason:  "FakeApproval",
								Message: "This should be ignored",
							},
						},
					}
					return er
				},
				And(
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("foo", "bar"))),
					Not(HaveField("Status.Conditions", ContainElement(HaveField("Reason", "FakeApproval")))),
					Not(HaveField("Status.Conditions", ContainElement(HaveField("Message", "This should be ignored")))),
				),
				BeEquivalentTo(http.StatusOK),
			),

			Entry("replace with spec modifications should fail",
				func(er api.EnrollmentRequest) api.EnrollmentRequest {
					er.Spec.Csr = "fake-csr-data"
					return er
				},
				HaveField("Spec.Csr", Not(Equal("fake-csr-data"))),
				BeEquivalentTo(http.StatusBadRequest),
			),
		)
	})

	// PUT /api/v1/enrollmentrequests/{name}/status
	Context("Status subresource operations", func() {
		DescribeTable("should handle status updates based on context and approval state",
			func(approveFirst bool,
				isExternalRequest bool,
				statusUpdateFunc func(api.EnrollmentRequest) api.EnrollmentRequest,
				expectedERStatusMatcher types.GomegaMatcher,
				statusMatcher types.GomegaMatcher,
			) {
				ctx := suite.Ctx

				er := CreateTestER()
				erName := lo.FromPtr(er.Metadata.Name)

				By("creating initial EnrollmentRequest")
				created, status := suite.Handler.CreateEnrollmentRequest(ctx, er)
				Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
				Expect(created).ToNot(BeNil())

				// Approve first if requested
				var setupResult *api.EnrollmentRequest
				if approveFirst {
					By("approving the EnrollmentRequest first")
					mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{}, []string{}, nil)
					ctxApproval := context.WithValue(ctx, consts.MappedIdentityCtxKey, mappedIdentity)

					approval := api.EnrollmentRequestApproval{
						Approved: true,
						Labels:   &map[string]string{"approved": "true"},
					}

					_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, erName, approval)
					Expect(st.Code).To(BeEquivalentTo(http.StatusOK))
				}

				By("re-reading the ER to get the latest state")
				approved, st := suite.Handler.GetEnrollmentRequest(ctx, erName)
				Expect(st.Code).To(BeEquivalentTo(http.StatusOK))
				setupResult = approved

				// Prepare status update
				statusUpdate := statusUpdateFunc(*setupResult)

				if !isExternalRequest {
					ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
				}

				By("performing status update")
				updated, st := suite.Handler.ReplaceEnrollmentRequestStatus(ctx, erName, statusUpdate)

				Expect(st.Code).To(statusMatcher)
				if IsStatusSuccessful(&st) {
					Expect(updated).ToNot(BeNil())
					Expect(updated).To(expectedERStatusMatcher, "Status should match expected after update")
				}

				By("verifying persistence by reading back")
				final, st := suite.Handler.GetEnrollmentRequestStatus(ctx, erName)
				Expect(st.Code).To(BeEquivalentTo(http.StatusOK))
				Expect(final).To(expectedERStatusMatcher, "Status should match expected after read back")
			},
			Entry("internal request should always succeed",
				false, // Don't approve first
				false, // Internal request
				func(er api.EnrollmentRequest) api.EnrollmentRequest {
					er.Status = &api.EnrollmentRequestStatus{
						Conditions: []api.Condition{
							{
								Type:    api.ConditionTypeEnrollmentRequestApproved,
								Status:  api.ConditionStatusTrue,
								Reason:  "InternalApproval",
								Message: "Should be allowed",
							},
						},
					}
					return er
				},
				HaveField("Status.Conditions", ContainElement(And(
					HaveField("Reason", "InternalApproval"),
					HaveField("Message", "Should be allowed"),
				))),
				BeEquivalentTo(http.StatusOK),
			),

			Entry("external request after approval should succeed",
				true, // Approve first
				true, // External request
				func(er api.EnrollmentRequest) api.EnrollmentRequest {
					er.Status = &api.EnrollmentRequestStatus{
						Conditions: []api.Condition{
							{
								Type:    api.ConditionTypeEnrollmentRequestApproved,
								Status:  api.ConditionStatusTrue,
								Reason:  "PostApprovalUpdate",
								Message: "Should be allowed after approval",
							},
						},
					}
					return er
				},
				HaveField("Status.Conditions", ContainElement(And(
					HaveField("Reason", "PostApprovalUpdate"),
					HaveField("Message", "Should be allowed after approval"),
				))),
				BeEquivalentTo(http.StatusOK),
			),
		)
	})

	// DELETE /api/v1/enrollmentrequests/{name}
	Context("Delete ER operations", func() {
		It("should allow deletion when no device exists", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			By("creating initial EnrollmentRequest")
			created, status := suite.Handler.CreateEnrollmentRequest(suite.Ctx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("deleting the EnrollmentRequest when no device exists")
			status = suite.Handler.DeleteEnrollmentRequest(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying the EnrollmentRequest is deleted")
			_, status = suite.Handler.GetEnrollmentRequest(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusNotFound))
		})

		It("should prevent deletion when live device exists", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			By("creating initial EnrollmentRequest")
			created, status := suite.Handler.CreateEnrollmentRequest(suite.Ctx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("creating a device with the same name")
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: &erName,
				},
			}
			_, deviceStatus := suite.Handler.CreateDevice(suite.Ctx, device)
			Expect(deviceStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			By("attempting to delete the EnrollmentRequest")
			status = suite.Handler.DeleteEnrollmentRequest(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusConflict))
			Expect(status.Message).To(ContainSubstring("device exists"))

			By("verifying the EnrollmentRequest still exists")
			retrieved, status := suite.Handler.GetEnrollmentRequest(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(retrieved).ToNot(BeNil())
		})

	})

	// POST /api/v1/enrollmentrequests/{name}/approval
	Context("Approval operations", func() {
		It("should handle approval operations and protect approval immutability", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			// Set up identity context
			mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{}, []string{}, nil)
			ctx := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			By("creating initial EnrollmentRequest")
			created, status := suite.Handler.CreateEnrollmentRequest(ctx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("creating initial approval and verifying it succeeds")
			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"env": "integration"},
			}
			_, st := suite.Handler.ApproveEnrollmentRequest(ctx, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying initial approval was applied correctly")
			approved, st := suite.Handler.GetEnrollmentRequest(ctx, erName)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(approved).To(And(
				HaveField("Status.Approval.Approved", BeTrue()),
				HaveField("Status.Approval.ApprovedBy", Equal("testuser")),
				HaveField("Status.Approval.Labels", PointTo(HaveKeyWithValue("env", "integration"))),
			))

			By("trying to approve again and verifying it fails")
			secondApproval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"second": "attempt"},
			}
			_, st = suite.Handler.ApproveEnrollmentRequest(ctx, erName, secondApproval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusBadRequest), "subsequent approvals should fail")

			By("verifying state is unchanged after failed second approval")
			afterSecond, st := suite.Handler.GetEnrollmentRequest(ctx, erName)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(afterSecond).To(And(
				HaveField("Status.Approval.Approved", BeTrue()),
				HaveField("Status.Approval.Labels", PointTo(HaveKeyWithValue("env", "integration"))),
				Not(HaveField("Status.Approval.Labels", PointTo(HaveKeyWithValue("second", "attempt")))),
			))

			By("trying to change approval from true to false and verifying it fails")
			denial := api.EnrollmentRequestApproval{
				Approved: false,
				Labels:   &map[string]string{"denied": "later"},
			}
			_, st = suite.Handler.ApproveEnrollmentRequest(ctx, erName, denial)
			// This should either fail or be ignored, but approval state should remain true

			By("verifying denial attempt did not change approval state")
			final, st := suite.Handler.GetEnrollmentRequest(ctx, erName)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(final).To(And(
				HaveField("Status.Approval.Approved", BeTrue()), // Should remain true
				HaveField("Status.Approval.Labels", PointTo(HaveKeyWithValue("env", "integration"))),
				Not(HaveField("Status.Approval.Labels", PointTo(HaveKeyWithValue("denied", "later")))),
			))
		})
	})

	// POST /api/v1/enrollmentrequests - knownRenderedVersion functionality
	Context("CreateEnrollmentRequest with knownRenderedVersion", func() {
		It("should add awaitingReconnect annotation when knownRenderedVersion is provided and not '0'", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			// Set knownRenderedVersion to a non-zero value
			knownRenderedVersion := "5"
			er.Spec.KnownRenderedVersion = &knownRenderedVersion

			By("creating enrollment request with knownRenderedVersion")
			created, status := suite.Handler.CreateEnrollmentRequest(suite.Ctx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())
			Expect(created.Metadata.Annotations).ToNot(BeNil())
			Expect(*created.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))

			By("verifying the annotation persists after read back")
			retrieved, status := suite.Handler.GetEnrollmentRequest(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(retrieved.Metadata.Annotations).ToNot(BeNil())
			Expect(*retrieved.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
		})

		It("should not add awaitingReconnect annotation when knownRenderedVersion is '0'", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			// Set knownRenderedVersion to "0"
			knownRenderedVersion := "0"
			er.Spec.KnownRenderedVersion = &knownRenderedVersion

			By("creating enrollment request with knownRenderedVersion='0'")
			created, status := suite.Handler.CreateEnrollmentRequest(suite.Ctx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			// Should not have awaitingReconnect annotation
			if created.Metadata.Annotations != nil {
				Expect(*created.Metadata.Annotations).ToNot(HaveKey(api.DeviceAnnotationAwaitingReconnect))
			}

			By("verifying no annotation after read back")
			retrieved, status := suite.Handler.GetEnrollmentRequest(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			if retrieved.Metadata.Annotations != nil {
				Expect(*retrieved.Metadata.Annotations).ToNot(HaveKey(api.DeviceAnnotationAwaitingReconnect))
			}
		})

		It("should not add awaitingReconnect annotation when knownRenderedVersion is empty string", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			// Set knownRenderedVersion to empty string
			knownRenderedVersion := ""
			er.Spec.KnownRenderedVersion = &knownRenderedVersion

			By("creating enrollment request with empty knownRenderedVersion")
			created, status := suite.Handler.CreateEnrollmentRequest(suite.Ctx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			// Should not have awaitingReconnect annotation
			if created.Metadata.Annotations != nil {
				Expect(*created.Metadata.Annotations).ToNot(HaveKey(api.DeviceAnnotationAwaitingReconnect))
			}

			By("verifying no annotation after read back")
			retrieved, status := suite.Handler.GetEnrollmentRequest(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			if retrieved.Metadata.Annotations != nil {
				Expect(*retrieved.Metadata.Annotations).ToNot(HaveKey(api.DeviceAnnotationAwaitingReconnect))
			}
		})

		It("should not add awaitingReconnect annotation when knownRenderedVersion is nil", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)

			// knownRenderedVersion is nil by default

			By("creating enrollment request without knownRenderedVersion")
			created, status := suite.Handler.CreateEnrollmentRequest(suite.Ctx, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			// Should not have awaitingReconnect annotation
			if created.Metadata.Annotations != nil {
				Expect(*created.Metadata.Annotations).ToNot(HaveKey(api.DeviceAnnotationAwaitingReconnect))
			}

			By("verifying no annotation after read back")
			retrieved, status := suite.Handler.GetEnrollmentRequest(suite.Ctx, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			if retrieved.Metadata.Annotations != nil {
				Expect(*retrieved.Metadata.Annotations).ToNot(HaveKey(api.DeviceAnnotationAwaitingReconnect))
			}
		})

	})
})
