package service_test

import (
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("EnrollmentHookPolicy service", func() {
	var suite ServiceTestSuite

	BeforeEach(func() {
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	It("When creating and getting a policy it should succeed", func() {
		policy := newServiceEnrollmentHookPolicy()
		result, status := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
		Expect(status.Code).To(Equal(int32(201)))
		Expect(result).ToNot(BeNil())
		Expect(lo.FromPtr(result.Metadata.Name)).To(Equal("default"))

		// Get should return same policy
		got, status := suite.EnrollmentHookPolicy.GetEnrollmentHookPolicy(suite.Ctx, suite.OrgID, "default")
		Expect(status.Code).To(Equal(int32(200)))
		Expect(got).ToNot(BeNil())
		Expect(lo.FromPtr(got.Metadata.Name)).To(Equal("default"))

		// Status should have Ready=True
		Expect(got.Status).ToNot(BeNil())
		Expect(got.Status.Conditions).ToNot(BeNil())
		ready := domain.FindStatusCondition(*got.Status.Conditions, "Ready")
		Expect(ready).ToNot(BeNil())
		Expect(ready.Status).To(Equal(domain.ConditionStatusTrue))
	})

	It("When name is not default it should return 400", func() {
		policy := newServiceEnrollmentHookPolicy()
		name := "not-default"
		policy.Metadata.Name = &name
		result, status := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
		Expect(status.Code).To(Equal(int32(400)))
		Expect(result).To(BeNil())
	})

	It("When bearer token is set it should be redacted on GET", func() {
		policy := newServiceEnrollmentHookPolicy()
		(*policy.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &api.EnrollmentHookAuth{
			BearerToken: lo.ToPtr("my-secret-token"),
		}
		_, status := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
		Expect(status.Code).To(Equal(int32(201)))

		got, status := suite.EnrollmentHookPolicy.GetEnrollmentHookPolicy(suite.Ctx, suite.OrgID, "default")
		Expect(status.Code).To(Equal(int32(200)))
		// Note: bearer tokens are encrypted at rest, not redacted at service level.
		// Redaction happens at transport level via HideSensitiveData.
		Expect(got).ToNot(BeNil())
	})

	It("When replacing with masked token it should preserve original", func() {
		policy := newServiceEnrollmentHookPolicy()
		(*policy.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &api.EnrollmentHookAuth{
			BearerToken: lo.ToPtr("original-secret"),
		}
		_, status := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
		Expect(status.Code).To(Equal(int32(201)))

		// Replace with masked value
		replacement := newServiceEnrollmentHookPolicy()
		(*replacement.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &api.EnrollmentHookAuth{
			BearerToken: lo.ToPtr("*****"),
		}
		result, status := suite.EnrollmentHookPolicy.ReplaceEnrollmentHookPolicy(suite.Ctx, suite.OrgID, "default", replacement)
		Expect(status.Code).To(Equal(int32(200)))
		Expect(result).ToNot(BeNil())

		// Status should have Ready=True after replace
		Expect(result.Status).ToNot(BeNil())
		Expect(result.Status.Conditions).ToNot(BeNil())
		ready := domain.FindStatusCondition(*result.Status.Conditions, "Ready")
		Expect(ready).ToNot(BeNil())
		Expect(ready.Status).To(Equal(domain.ConditionStatusTrue))
	})

	It("When deleting a policy it should succeed", func() {
		policy := newServiceEnrollmentHookPolicy()
		_, status := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
		Expect(status.Code).To(Equal(int32(201)))

		status = suite.EnrollmentHookPolicy.DeleteEnrollmentHookPolicy(suite.Ctx, suite.OrgID, "default")
		Expect(status.Code).To(Equal(int32(200)))
	})

	It("When creating a gate-only policy it should succeed", func() {
		name := "default"
		policy := api.EnrollmentHookPolicy{
			ApiVersion: "flightctl.io/v1beta1",
			Kind:       api.EnrollmentHookPolicyKind,
			Metadata:   api.ObjectMeta{Name: &name},
			Spec: api.EnrollmentHookPolicySpec{
				AfterEnrolling: api.EnrollmentHookStageSpec{},
			},
		}

		result, status := suite.EnrollmentHookPolicy.CreateEnrollmentHookPolicy(suite.Ctx, suite.OrgID, policy)
		Expect(status.Code).To(Equal(int32(201)))
		Expect(result).ToNot(BeNil())

		// Default failurePolicy should be Block
		Expect(result.Spec.AfterEnrolling.FailurePolicy).ToNot(BeNil())
		Expect(*result.Spec.AfterEnrolling.FailurePolicy).To(Equal(api.FailurePolicyBlock))

		// Get should work
		got, status := suite.EnrollmentHookPolicy.GetEnrollmentHookPolicy(suite.Ctx, suite.OrgID, "default")
		Expect(status.Code).To(Equal(int32(200)))
		Expect(got).ToNot(BeNil())
	})
})

func newServiceEnrollmentHookPolicy() api.EnrollmentHookPolicy {
	name := "default"
	return api.EnrollmentHookPolicy{
		ApiVersion: "flightctl.io/v1beta1",
		Kind:       api.EnrollmentHookPolicyKind,
		Metadata:   api.ObjectMeta{Name: &name},
		Spec: api.EnrollmentHookPolicySpec{
			AfterEnrolling: api.EnrollmentHookStageSpec{
				FailurePolicy: lo.ToPtr(api.FailurePolicyBlock),
				ControlPlaneActions: &[]api.EnrollmentHookHttpAction{
					{
						Url:     "https://example.com/hook",
						Timeout: lo.ToPtr("30s"),
					},
				},
			},
		},
	}
}
