package service_test

import (
	"net/http"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/onsi/gomega/types"
	"github.com/samber/lo"
)

var _ = Describe("CertificateSigningRequest Integration Tests", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		By("setting up CertificateSigningRequest Service Integration Tests")
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	// PATCH /api/v1/certificatesigningrequests/{name}
	Context("Patch CSR operations", func() {
		DescribeTable("should handle patch operations correctly", func(patch api.PatchRequest, patchedMatcher types.GomegaMatcher, statusMatcher types.GomegaMatcher) {
			csr := CreateTestCSR()
			csrName := lo.FromPtr(csr.Metadata.Name)

			By("creating a test CSR")
			created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, csr)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())

			By("applying patch operation")
			patched, status := suite.CertificateSigningRequest.PatchCertificateSigningRequest(suite.Ctx, suite.OrgID, csrName, patch)

			Expect(status.Code).To(statusMatcher)
			if IsStatusSuccessful(&status) {
				Expect(patched).ToNot(BeNil())
				// Verify immutable spec fields and status consistency
				VerifyCSRSpecImmutability(patched, created)
				// Verify expected changes
				if patchedMatcher != nil {
					Expect(patched).To(patchedMatcher)
				}
			}

			By("verifying persistence after read back")
			retrieved, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, csrName)
			VerifyCSRSpecImmutability(retrieved, created)
			if patchedMatcher != nil {
				Expect(retrieved).To(patchedMatcher, "should match expected patch result after read back")
			}

		},
			Entry("metadata label patch",
				NewLabelPatch("foo", "bar"),
				And(
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("foo", "bar"))),
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("test", "integration"))),
				),
				BeEquivalentTo(http.StatusOK)),

			Entry("multiple metadata operations",
				NewMultiLabelPatch(
					map[string]string{"environment": "staging"},
					map[string]string{"test": "updated"},
				),
				And(
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("environment", "staging"))),
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("test", "updated"))),
				),
				BeEquivalentTo(http.StatusOK)),

			Entry("attempt to modify name should fail",
				api.PatchRequest{
					{
						Op:    "replace",
						Path:  "/metadata/name",
						Value: "new-name",
					},
				},
				nil, // Don't care about result on failure
				BeEquivalentTo(http.StatusBadRequest)),

			Entry("attempt to modify resourceVersion should be ignored or fail",
				api.PatchRequest{
					{
						Op:    "replace",
						Path:  "/metadata/resourceVersion",
						Value: "999",
					},
				},
				HaveField("Metadata.ResourceVersion", Not(Equal("999"))),
				Or(BeEquivalentTo(http.StatusOK), BeEquivalentTo(http.StatusBadRequest))),

			Entry("attempt to modify spec signerName should be ignored or fail",
				api.PatchRequest{
					{
						Op:    "replace",
						Path:  "/spec/signerName",
						Value: "fake-signer",
					},
				},
				nil, // Spec immutability verified by VerifyCSRSpecImmutability
				Or(BeEquivalentTo(http.StatusOK), BeEquivalentTo(http.StatusBadRequest))),

			Entry("attempt to modify spec request should be ignored or fail",
				api.PatchRequest{
					{
						Op:    "replace",
						Path:  "/spec/request",
						Value: "fake-csr-data",
					},
				},
				nil, // Spec immutability verified by VerifyCSRSpecImmutability
				Or(BeEquivalentTo(http.StatusOK), BeEquivalentTo(http.StatusBadRequest))),

			Entry("attempt to modify spec usages should be ignored or fail",
				api.PatchRequest{
					{
						Op:    "replace",
						Path:  "/spec/usages",
						Value: []string{"fakeUsage"},
					},
				},
				HaveField("Spec.Usages", PointTo(Not(ContainElement("fakeUsage")))),
				Or(BeEquivalentTo(http.StatusOK), BeEquivalentTo(http.StatusBadRequest))),

			Entry("status patch should be ignored or fail",
				api.PatchRequest{
					{
						Op:   "add",
						Path: "/status",
						Value: api.CertificateSigningRequestStatus{
							Conditions: []api.Condition{
								{
									Type:    api.ConditionTypeCertificateSigningRequestDenied,
									Status:  api.ConditionStatusFalse,
									Reason:  "ExternalDenial",
									Message: "Denied via patch",
								},
							},
						},
					},
				},
				Not(HaveField("Status.Conditions", ContainElement(HaveField("Type", api.ConditionTypeCertificateSigningRequestDenied)))),
				Or(BeEquivalentTo(http.StatusOK), BeEquivalentTo(http.StatusBadRequest))),

			Entry("invalid patch path should fail",
				api.PatchRequest{
					{
						Op:    "add",
						Path:  "/nonexistent/field",
						Value: "value",
					},
				},
				nil, // Don't care about result on failure
				BeEquivalentTo(http.StatusBadRequest)),
		)
	})

	// PUT /api/v1/certificatesigningrequests/{name}
	Context("Replace CSR operations", func() {
		DescribeTable("should handle replace operations correctly",
			func(mapCSR func(api.CertificateSigningRequest) api.CertificateSigningRequest, replacedMatcher types.GomegaMatcher, statusMatcher types.GomegaMatcher) {
				csr := CreateTestCSR()
				csrName := lo.FromPtr(csr.Metadata.Name)

				By("creating a test CSR")
				created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, csr)
				Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

				By("retrieving CSR for replacement")
				retrieved, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, csrName)
				Expect(status.Code).To(BeEquivalentTo(http.StatusOK))

				replacement := mapCSR(*retrieved)

				By("performing replacement operation")
				replaced, status := suite.CertificateSigningRequest.ReplaceCertificateSigningRequest(suite.Ctx, suite.OrgID, csrName, replacement)
				Expect(status.Code).To(statusMatcher)

				if IsStatusSuccessful(&status) {
					Expect(replaced).ToNot(BeNil())

					// Verify immutable spec fields and status consistency
					VerifyCSRSpecImmutability(replaced, created)

					// Verify expected changes
					if replacedMatcher != nil {
						Expect(replaced).To(replacedMatcher)
					}
				}

				By("verifying persistence after read back")
				final, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, csrName)
				Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
				VerifyCSRSpecImmutability(final, created)
				if replacedMatcher != nil {
					Expect(final).To(replacedMatcher, "should match expected replace result after read back")
				}
			},
			Entry("normal replace with labels",
				func(csr api.CertificateSigningRequest) api.CertificateSigningRequest {
					csr.Metadata.Labels = &map[string]string{
						"test":        "integration",
						"environment": "production",
						"version":     "v2",
					}
					return csr
				},
				And(
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("environment", "production"))),
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("version", "v2"))),
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("test", "integration"))),
				),
				BeEquivalentTo(http.StatusOK)),

			Entry("replace with status should ignore status",
				func(csr api.CertificateSigningRequest) api.CertificateSigningRequest {
					csr.Metadata.Labels = &map[string]string{
						"test": "integration",
					}
					csr.Status = &api.CertificateSigningRequestStatus{
						Conditions: []api.Condition{
							{
								Type:    api.ConditionTypeCertificateSigningRequestApproved,
								Status:  api.ConditionStatusTrue,
								Reason:  "FakeApproval",
								Message: "This should be ignored",
							},
						},
					}
					return csr
				},
				And(
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("test", "integration"))),
					Not(HaveField("Status.Conditions", ContainElement(HaveField("Reason", "FakeApproval")))),
				),
				BeEquivalentTo(http.StatusOK)),

			Entry("replace with spec modifications should fail",
				func(csr api.CertificateSigningRequest) api.CertificateSigningRequest {
					csr.Spec.Usages = &[]string{"fakeUsage"}
					return csr
				},
				HaveField("Spec.Usages", PointTo(Not(ContainElement("fakeUsage")))),
				BeEquivalentTo(http.StatusBadRequest)),

			Entry("replace with denied status should ignore status",
				func(csr api.CertificateSigningRequest) api.CertificateSigningRequest {
					csr.Metadata.Labels = &map[string]string{
						"test": "integration",
					}
					csr.Status = &api.CertificateSigningRequestStatus{
						Conditions: []api.Condition{
							{
								Type:    api.ConditionTypeCertificateSigningRequestDenied,
								Status:  api.ConditionStatusFalse,
								Reason:  "ExternalDenial",
								Message: "Denied via replace",
							},
						},
					}
					return csr
				},
				And(
					HaveField("Metadata.Labels", PointTo(HaveKeyWithValue("test", "integration"))),
					Not(HaveField("Status.Conditions", ContainElement(HaveField("Reason", "ExternalDenial")))),
				),
				BeEquivalentTo(http.StatusOK)),
		)
	})

	Context("Prevent denied CSR from losing status on replace", func() {
		It("should preserve denied status when replacing a denied CSR", func() {
			csr := CreateTestCSR()
			// Modify the name to create a CN mismatch which will cause signing failure
			originalName := lo.FromPtr(csr.Metadata.Name)
			modifiedName := originalName + "-modified"
			csr.Metadata.Name = &modifiedName
			csrName := modifiedName

			By("creating a test CSR with enrollment signer (auto-approval) and mismatched name")
			_, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, csr)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			By("verifying it was auto-approved but signing failed (no certificate)")
			retrieved, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, csrName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(api.IsStatusConditionTrue(retrieved.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved)).To(BeTrue())
			// Should have Failed condition due to CN mismatch
			Expect(api.IsStatusConditionTrue(retrieved.Status.Conditions, api.ConditionTypeCertificateSigningRequestFailed)).To(BeTrue())
			// Should NOT have a certificate
			Expect(retrieved.Status.Certificate).To(Or(BeNil(), PointTo(BeEmpty())))

			By("denying the auto-approved but failed CSR")
			api.SetStatusCondition(&retrieved.Status.Conditions, api.Condition{
				Type:    api.ConditionTypeCertificateSigningRequestDenied,
				Status:  api.ConditionStatusTrue,
				Reason:  "AdminDenied",
				Message: "Manually denied by admin after signing failure",
			})
			api.RemoveStatusCondition(&retrieved.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved)
			api.RemoveStatusCondition(&retrieved.Status.Conditions, api.ConditionTypeCertificateSigningRequestFailed)

			denied, status := suite.CertificateSigningRequest.UpdateCertificateSigningRequestApproval(suite.Ctx, suite.OrgID, csrName, *retrieved)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(api.IsStatusConditionTrue(denied.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied)).To(BeTrue())
			Expect(api.IsStatusConditionTrue(denied.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved)).To(BeFalse())

			By("attempting to replace the denied CSR with metadata change")
			replacement := *denied
			replacement.Metadata.Labels = &map[string]string{
				"updated": "true",
				"test":    "preserve-denied-status",
			}

			replaced, status := suite.CertificateSigningRequest.ReplaceCertificateSigningRequest(suite.Ctx, suite.OrgID, csrName, replacement)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(replaced).ToNot(BeNil())

			By("verifying the denied status is preserved and NOT auto-approved again")
			Expect(replaced.Metadata.Labels).To(PointTo(HaveKeyWithValue("updated", "true")))
			Expect(api.IsStatusConditionTrue(replaced.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied)).To(BeTrue(), "Denied status should be preserved")
			Expect(api.IsStatusConditionTrue(replaced.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved)).To(BeFalse(), "Should NOT be auto-approved again")
		})

		It("should preserve approved status when replacing an approved CSR", func() {
			csr := CreateTestCSR()
			csrName := lo.FromPtr(csr.Metadata.Name)

			By("creating and approving a test CSR")
			_, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, csr)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			// It should be auto-approved since it has enrollment signer
			retrieved, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, csrName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(api.IsStatusConditionTrue(retrieved.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved)).To(BeTrue())

			By("replacing the approved CSR with metadata change")
			replacement := *retrieved
			replacement.Metadata.Labels = &map[string]string{
				"updated": "true",
			}

			replaced, status := suite.CertificateSigningRequest.ReplaceCertificateSigningRequest(suite.Ctx, suite.OrgID, csrName, replacement)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(replaced).ToNot(BeNil())

			By("verifying the approved status is preserved")
			Expect(replaced.Metadata.Labels).To(PointTo(HaveKeyWithValue("updated", "true")))
			Expect(api.IsStatusConditionTrue(replaced.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved)).To(BeTrue(), "Approved status should be preserved")
		})
	})

	Context("Enrollment credential issuance", func() {
		BeforeEach(func() {
			suite.Ctx = util.WithOrganizationID(suite.Ctx, suite.OrgID)
		})

		It("When issuing an enrollment credential it should persist its identity and issuance time", func() {
			request := CreateTestCSR()
			created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created.Status.Certificate).NotTo(BeNil())
			Expect(created.Status.IssuedAt).NotTo(BeNil())
			Expect(*created.Status.IssuedAt).To(BeTemporally("~", time.Now().UTC(), time.Minute))

			loaded, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(loaded.Status).To(Equal(created.Status))
		})

		It("When enrollment issuance races it should preserve the first committed evidence", func() {
			provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
			request := CreateTestCSR()
			created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
			Expect(err).NotTo(HaveOccurred())
			Expect(created.Status.Certificate).To(BeNil())

			type approvalResult struct {
				csr    *api.CertificateSigningRequest
				status api.Status
			}
			const writers = 4
			start := make(chan struct{})
			results := make(chan approvalResult, writers)
			var wait sync.WaitGroup
			for range writers {
				wait.Add(1)
				go func() {
					defer wait.Done()
					<-start
					approval := *created
					approval.Status = &api.CertificateSigningRequestStatus{Conditions: []api.Condition{{
						Type:    api.ConditionTypeCertificateSigningRequestApproved,
						Status:  api.ConditionStatusTrue,
						Reason:  "Approved",
						Message: "Concurrent approval",
					}}}
					csr, status := suite.CertificateSigningRequest.UpdateCertificateSigningRequestApproval(
						suite.Ctx, suite.OrgID, lo.FromPtr(created.Metadata.Name), approval)
					results <- approvalResult{csr: csr, status: status}
				}()
			}
			close(start)
			wait.Wait()
			close(results)

			var successes []*api.CertificateSigningRequest
			for result := range results {
				if result.status.Code != http.StatusOK {
					Expect(result.status.Code).To(BeEquivalentTo(http.StatusConflict))
					continue
				}
				Expect(result.csr).NotTo(BeNil())
				Expect(result.csr.Status.Certificate).NotTo(BeNil())
				Expect(result.csr.Status.IssuedAt).NotTo(BeNil())
				successes = append(successes, result.csr)
			}
			Expect(successes).NotTo(BeEmpty())

			stored, err := provider.Get(suite.Ctx, suite.OrgID, lo.FromPtr(created.Metadata.Name))
			Expect(err).NotTo(HaveOccurred())
			Expect(stored.Status.Certificate).NotTo(BeNil())
			Expect(stored.Status.IssuedAt).NotTo(BeNil())
			for _, result := range successes {
				Expect(result.Status.Certificate).To(Equal(stored.Status.Certificate))
				Expect(result.Status.IssuedAt).To(Equal(stored.Status.IssuedAt))
			}
		})
	})

})
