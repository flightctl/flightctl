package v1beta1

import (
	"encoding/json"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestEnrollmentApprovalPolicyStatusAlwaysIncludesCount(t *testing.T) {
	encoded, err := json.Marshal(EnrollmentApprovalPolicyStatus{})
	require.NoError(t, err)
	require.JSONEq(t, `{"enrollmentCount":0}`, string(encoded))
}

func TestEnrollmentApprovalPolicyStatusIsReadOnly(t *testing.T) {
	specification, err := GetSpec()
	require.NoError(t, err)
	statusPath := specification.Paths.Find("/enrollmentapprovalpolicies/{name}/status")
	require.NotNil(t, statusPath)
	require.NotNil(t, statusPath.Get)
	require.Nil(t, statusPath.Put)
	require.Nil(t, statusPath.Patch)
	require.Nil(t, statusPath.Post)
	require.Nil(t, statusPath.Delete)
}

func TestEnrollmentApprovalPolicyValidation(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*EnrollmentApprovalPolicy)
		invalid bool
	}{
		{name: "When defaults are used it should accept a disabled policy", change: func(*EnrollmentApprovalPolicy) {}},
		{name: "When quota is zero it should accept", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.ApprovalLimit = lo.ToPtr(int64(0)) }},
		{name: "When quota is negative it should reject", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.ApprovalLimit = lo.ToPtr(int64(-1)) }, invalid: true},
		{name: "When window is zero it should reject", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.AutoApprovalWindowDays = lo.ToPtr(int32(0)) }, invalid: true},
		{name: "When window overflows duration it should reject", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.AutoApprovalWindowDays = lo.ToPtr(int32(106752)) }, invalid: true},
		{name: "When window is maximal it should accept", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.AutoApprovalWindowDays = lo.ToPtr(int32(106751)) }},
		{name: "When certificate is missing it should reject", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.CertificateName = "" }, invalid: true},
		{name: "When expression is blank it should reject", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.MatchExpression = " " }, invalid: true},
		{name: "When required device labels are absent it should reject", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.DeviceLabels = nil }, invalid: true},
		{name: "When labels are invalid it should reject", change: func(policy *EnrollmentApprovalPolicy) {
			policy.Spec.DeviceLabels = map[string]string{"invalid key": "value"}
		}, invalid: true},
		{name: "When incoming keys are wildcards it should reject", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.AllowedIncomingLabels = &[]string{"*"} }, invalid: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := EnrollmentApprovalPolicy{Metadata: ObjectMeta{Name: lo.ToPtr("west")}, Spec: EnrollmentApprovalPolicySpec{CertificateName: "commissioning", MatchExpression: "true", DeviceLabels: map[string]string{}}}
			testCase.change(&policy)
			if testCase.invalid {
				require.NotEmpty(t, policy.Validate())
			} else {
				require.Empty(t, policy.Validate())
			}
		})
	}
}
