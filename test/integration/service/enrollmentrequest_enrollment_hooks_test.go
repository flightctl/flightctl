package service_test

import (
	"context"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

func approvalContext(suite *ServiceTestSuite) context.Context {
	mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{}, map[string][]string{}, false, nil)
	return context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)
}

func approveER(suite *ServiceTestSuite, ctx context.Context, name string) {
	approval := api.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"env": "test"},
	}
	_, st := suite.EnrollmentRequest.ApproveEnrollmentRequest(ctx, suite.OrgID, name, approval)
	ExpectWithOffset(1, st.Code).To(BeEquivalentTo(http.StatusOK))
}

var _ = Describe("EnrollmentRequest EnrollmentHooks Integration", func() {
	var suite *ServiceTestSuite
	var ctx context.Context

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		ctx = approvalContext(suite)
	})

	AfterEach(func() {
		suite.Teardown()
	})

	Context("Approve with notify policy", func() {
		It("When approving with controlPlaneActions it should set EnrollmentHooks=False/NotifyPending and write secrets", func() {
			By("creating a policy with bearer token actions")
			policy := api.EnrollmentHookPolicy{
				ApiVersion: "flightctl.io/v1beta1",
				Kind:       api.EnrollmentHookPolicyKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr("default")},
				Spec: api.EnrollmentHookPolicySpec{
					AfterEnrolling: api.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(api.FailurePolicyBlock),
						ControlPlaneActions: &[]api.EnrollmentHookHttpAction{
							{
								Url:     "https://hooks.example.com/notify",
								Timeout: lo.ToPtr("30s"),
								Auth: &api.EnrollmentHookAuth{
									BearerToken: lo.ToPtr("secret-token-1"),
								},
								Retry: &api.EnrollmentHookRetryPolicy{
									MaxAttempts: lo.ToPtr(3),
								},
							},
						},
					},
				},
			}
			_, pSt := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
			Expect(pSt.Code).To(BeEquivalentTo(http.StatusCreated))

			By("creating and approving an enrollment request")
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			_, st := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, er)
			Expect(st.Code).To(BeEquivalentTo(http.StatusCreated))
			approveER(suite, ctx, erName)

			By("verifying device has EnrollmentHooks condition with NotifyPending")
			dev, devSt := suite.Device.GetDevice(ctx, suite.OrgID, erName)
			Expect(devSt.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(dev).NotTo(BeNil())
			Expect(dev.Status).NotTo(BeNil())

			cond := domain.FindStatusCondition(dev.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(domain.ConditionStatusFalse))
			Expect(cond.Reason).To(Equal(domain.EnrollmentHooksReasonNotifyPending))

			By("verifying snapshot has URL and retry fields but no bearer token")
			Expect(dev.Status.EnrollmentHooks).NotTo(BeNil())
			Expect(dev.Status.EnrollmentHooks.Snapshot).NotTo(BeNil())
			Expect(dev.Status.EnrollmentHooks.Snapshot.FailurePolicy).To(Equal(api.FailurePolicyBlock))
			Expect(dev.Status.EnrollmentHooks.Snapshot.ControlPlaneActions).NotTo(BeNil())
			actions := *dev.Status.EnrollmentHooks.Snapshot.ControlPlaneActions
			Expect(actions).To(HaveLen(1))
			Expect(actions[0].Url).To(Equal("https://hooks.example.com/notify"))
			Expect(actions[0].Timeout).To(Equal(lo.ToPtr("30s")))
			Expect(actions[0].Retry).NotTo(BeNil())
			Expect(actions[0].Index).To(Equal(0))

			By("verifying secrets were written to the store")
			secrets, err := suite.NotifySecretsStore.ListByDevice(ctx, suite.OrgID, erName)
			Expect(err).NotTo(HaveOccurred())
			Expect(secrets).To(HaveLen(1))
			Expect(secrets[0].ActionIndex).To(Equal(0))
		})
	})

	Context("Agent status update after approve", func() {
		It("When an approved device receives an agent status update it should preserve EnrollmentHooks snapshot and condition", func() {
			By("creating a policy and approving an enrollment request")
			policy := api.EnrollmentHookPolicy{
				ApiVersion: "flightctl.io/v1beta1",
				Kind:       api.EnrollmentHookPolicyKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr("default")},
				Spec: api.EnrollmentHookPolicySpec{
					AfterEnrolling: api.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(api.FailurePolicyBlock),
						ControlPlaneActions: &[]api.EnrollmentHookHttpAction{
							{
								Url: "https://hooks.example.com/notify",
								Auth: &api.EnrollmentHookAuth{
									BearerToken: lo.ToPtr("secret-token-1"),
								},
							},
						},
					},
				},
			}
			_, pSt := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
			Expect(pSt.Code).To(BeEquivalentTo(http.StatusCreated))

			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			_, st := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, er)
			Expect(st.Code).To(BeEquivalentTo(http.StatusCreated))
			approveER(suite, ctx, erName)

			By("simulating an agent heartbeat that replaces device status")
			agentStatus := domain.NewDeviceStatus()
			agentStatus.SystemInfo = api.DeviceSystemInfo{
				OperatingSystem: "linux",
				Architecture:    "amd64",
				AgentVersion:    "v1.0.0",
			}
			agentDevice := domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr(erName)},
				Status:   &agentStatus,
			}
			_, replaceSt := suite.Device.ReplaceDeviceStatus(ctx, suite.OrgID, erName, agentDevice, true)
			Expect(replaceSt.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying EnrollmentHooks snapshot and condition survived the status replace")
			dev, devSt := suite.Device.GetDevice(ctx, suite.OrgID, erName)
			Expect(devSt.Code).To(BeEquivalentTo(http.StatusOK))

			cond := domain.FindStatusCondition(dev.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(domain.ConditionStatusFalse))
			Expect(cond.Reason).To(Equal(domain.EnrollmentHooksReasonNotifyPending))

			Expect(dev.Status.EnrollmentHooks).NotTo(BeNil())
			Expect(dev.Status.EnrollmentHooks.Snapshot).NotTo(BeNil())
			Expect(dev.Status.EnrollmentHooks.Snapshot.FailurePolicy).To(Equal(api.FailurePolicyBlock))
			Expect(dev.Status.EnrollmentHooks.Snapshot.ControlPlaneActions).NotTo(BeNil())
			Expect(*dev.Status.EnrollmentHooks.Snapshot.ControlPlaneActions).To(HaveLen(1))
			Expect((*dev.Status.EnrollmentHooks.Snapshot.ControlPlaneActions)[0].Url).To(Equal("https://hooks.example.com/notify"))
		})
	})

	Context("Approve with gate-only policy (TC-FR11-02)", func() {
		It("When approving with gate-only policy it should set EnrollmentHooks=False/Pending", func() {
			By("creating a gate-only policy (no controlPlaneActions)")
			policy := api.EnrollmentHookPolicy{
				ApiVersion: "flightctl.io/v1beta1",
				Kind:       api.EnrollmentHookPolicyKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr("default")},
				Spec: api.EnrollmentHookPolicySpec{
					AfterEnrolling: api.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(api.FailurePolicyBlock),
					},
				},
			}
			_, pSt := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
			Expect(pSt.Code).To(BeEquivalentTo(http.StatusCreated))

			By("approving an enrollment request")
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			_, st := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, er)
			Expect(st.Code).To(BeEquivalentTo(http.StatusCreated))
			approveER(suite, ctx, erName)

			By("verifying EnrollmentHooks=False/Pending")
			dev, devSt := suite.Device.GetDevice(ctx, suite.OrgID, erName)
			Expect(devSt.Code).To(BeEquivalentTo(http.StatusOK))

			cond := domain.FindStatusCondition(dev.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(domain.ConditionStatusFalse))
			Expect(cond.Reason).To(Equal(domain.EnrollmentHooksReasonPending))
		})
	})

	Context("Approve without policy (TC-FR11-03)", func() {
		It("When no policy exists it should not set EnrollmentHooks condition", func() {
			By("approving without any policy")
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			_, st := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, er)
			Expect(st.Code).To(BeEquivalentTo(http.StatusCreated))
			approveER(suite, ctx, erName)

			By("verifying no EnrollmentHooks condition")
			dev, devSt := suite.Device.GetDevice(ctx, suite.OrgID, erName)
			Expect(devSt.Code).To(BeEquivalentTo(http.StatusOK))

			cond := domain.FindStatusCondition(dev.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
			Expect(cond).To(BeNil())
			Expect(dev.Status.EnrollmentHooks).To(BeNil())
		})
	})

	Context("Snapshot immutability (TC-FR11-04)", func() {
		It("When policy is edited after first approve, first device snapshot should be unchanged", func() {
			By("creating initial policy")
			policy := api.EnrollmentHookPolicy{
				ApiVersion: "flightctl.io/v1beta1",
				Kind:       api.EnrollmentHookPolicyKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr("default")},
				Spec: api.EnrollmentHookPolicySpec{
					AfterEnrolling: api.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(api.FailurePolicyBlock),
						ControlPlaneActions: &[]api.EnrollmentHookHttpAction{
							{
								Url:     "https://original.example.com/hook",
								Timeout: lo.ToPtr("30s"),
								Auth: &api.EnrollmentHookAuth{
									BearerToken: lo.ToPtr("original-token"),
								},
							},
						},
					},
				},
			}
			createdPolicy, pSt := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
			Expect(pSt.Code).To(BeEquivalentTo(http.StatusCreated))

			By("approving device A")
			erA := CreateTestER()
			nameA := lo.FromPtr(erA.Metadata.Name)
			_, st := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, erA)
			Expect(st.Code).To(BeEquivalentTo(http.StatusCreated))
			approveER(suite, ctx, nameA)

			By("editing the policy URL")
			updatedPolicy := *createdPolicy
			(*updatedPolicy.Spec.AfterEnrolling.ControlPlaneActions)[0].Url = "https://updated.example.com/hook"
			(*updatedPolicy.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &api.EnrollmentHookAuth{
				BearerToken: lo.ToPtr("updated-token"),
			}
			_, pSt = suite.EnrollmentHookPolicy.ReplaceEnrollmentHookPolicy(suite.Ctx, suite.OrgID, "default", updatedPolicy)
			Expect(pSt.Code).To(BeEquivalentTo(http.StatusOK))

			By("approving device B")
			erB := CreateTestER()
			nameB := lo.FromPtr(erB.Metadata.Name)
			_, st = suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, erB)
			Expect(st.Code).To(BeEquivalentTo(http.StatusCreated))
			approveER(suite, ctx, nameB)

			By("verifying device A still has original snapshot")
			devA, devSt := suite.Device.GetDevice(ctx, suite.OrgID, nameA)
			Expect(devSt.Code).To(BeEquivalentTo(http.StatusOK))
			actionsA := *devA.Status.EnrollmentHooks.Snapshot.ControlPlaneActions
			Expect(actionsA[0].Url).To(Equal("https://original.example.com/hook"))

			By("verifying device B has updated snapshot")
			devB, devSt := suite.Device.GetDevice(ctx, suite.OrgID, nameB)
			Expect(devSt.Code).To(BeEquivalentTo(http.StatusOK))
			actionsB := *devB.Status.EnrollmentHooks.Snapshot.ControlPlaneActions
			Expect(actionsB[0].Url).To(Equal("https://updated.example.com/hook"))

			By("verifying secrets exist for device A")
			secretsA, err := suite.NotifySecretsStore.ListByDevice(ctx, suite.OrgID, nameA)
			Expect(err).NotTo(HaveOccurred())
			Expect(secretsA).To(HaveLen(1))
		})
	})

	Context("Purge on device deletion (AC-5)", func() {
		It("When device is deleted it should purge notify secrets", func() {
			By("creating a policy with secrets")
			policy := api.EnrollmentHookPolicy{
				ApiVersion: "flightctl.io/v1beta1",
				Kind:       api.EnrollmentHookPolicyKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr("default")},
				Spec: api.EnrollmentHookPolicySpec{
					AfterEnrolling: api.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(api.FailurePolicyBlock),
						ControlPlaneActions: &[]api.EnrollmentHookHttpAction{
							{
								Url: "https://hooks.example.com/notify",
								Auth: &api.EnrollmentHookAuth{
									BearerToken: lo.ToPtr("purge-test-token"),
								},
							},
						},
					},
				},
			}
			_, pSt := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
			Expect(pSt.Code).To(BeEquivalentTo(http.StatusCreated))

			By("creating and approving enrollment request")
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			_, st := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, er)
			Expect(st.Code).To(BeEquivalentTo(http.StatusCreated))
			approveER(suite, ctx, erName)

			By("verifying secrets exist before deletion")
			secrets, err := suite.NotifySecretsStore.ListByDevice(ctx, suite.OrgID, erName)
			Expect(err).NotTo(HaveOccurred())
			Expect(secrets).To(HaveLen(1))

			By("deleting the device")
			devSt := suite.Device.DeleteDevice(ctx, suite.OrgID, erName)
			Expect(devSt.Code).To(BeEquivalentTo(http.StatusOK))

			By("verifying secrets were purged")
			secrets, err = suite.NotifySecretsStore.ListByDevice(ctx, suite.OrgID, erName)
			Expect(err).NotTo(HaveOccurred())
			Expect(secrets).To(BeEmpty())
		})
	})
})
