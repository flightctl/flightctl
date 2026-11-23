package v1beta1

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestPolicyLabelErrorPaths(t *testing.T) {
	policy := EnrollmentApprovalPolicy{
		Metadata: ObjectMeta{Name: lo.ToPtr("west"), Labels: lo.ToPtr(map[string]string{"invalid metadata key": "value"})},
		Spec: EnrollmentApprovalPolicySpec{
			CertificateName: "credential", MatchExpression: "true",
			DeviceLabels: map[string]string{"invalid device key": "value"}, AllowedIncomingLabels: lo.ToPtr([]string{"*"}),
		},
	}
	err := errors.Join(policy.Validate()...)
	require.ErrorContains(t, err, "metadata.labels")
	require.ErrorContains(t, err, "spec.deviceLabels")
	require.ErrorContains(t, err, "spec.allowedIncomingLabels[0]")
}

func TestEnrollmentApprovalPolicyStatusAlwaysIncludesCount(t *testing.T) {
	encoded, err := json.Marshal(EnrollmentApprovalPolicyStatus{})
	require.NoError(t, err)
	require.JSONEq(t, `{"enrollmentCount":0}`, string(encoded))
}

func TestEnrollmentApprovalPolicyHasNoStatusEndpoint(t *testing.T) {
	specification, err := GetSpec()
	require.NoError(t, err)
	statusPath := specification.Paths.Find("/enrollmentapprovalpolicies/{name}/status")
	require.Nil(t, statusPath)
}

func TestEnrollmentApprovalPolicyValidation(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*EnrollmentApprovalPolicy)
		invalid bool
	}{
		{name: "When defaults are used it should accept a disabled policy", change: func(*EnrollmentApprovalPolicy) {}},
		{name: "When quota is zero it should accept", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.ApprovalLimit = lo.ToPtr(int32(0)) }},
		{name: "When quota is negative it should reject", change: func(policy *EnrollmentApprovalPolicy) { policy.Spec.ApprovalLimit = lo.ToPtr(int32(-1)) }, invalid: true},
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

func TestPolicyLimitDecoding(t *testing.T) {
	for _, tc := range []struct {
		name, body                 string
		decodeError, semanticError bool
		want                       *int32
	}{
		{"omitted", `{}`, false, false, nil},
		{"null", `{"approvalLimit":null}`, false, false, nil},
		{"zero", `{"approvalLimit":0}`, false, false, lo.ToPtr(int32(0))},
		{"maximum", `{"approvalLimit":2147483647}`, false, false, lo.ToPtr(int32(2147483647))},
		{"negative", `{"approvalLimit":-1}`, false, true, lo.ToPtr(int32(-1))},
		{"overflow", `{"approvalLimit":2147483648}`, true, false, nil},
		{"fractional", `{"approvalLimit":0.5}`, true, false, nil},
		{"wrong type", `{"approvalLimit":"1"}`, true, false, nil},
	} {
		t.Run("When the limit is "+tc.name+" it should enforce its input contract", func(t *testing.T) {
			var spec EnrollmentApprovalPolicySpec
			err := json.Unmarshal([]byte(tc.body), &spec)
			if tc.decodeError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, spec.ApprovalLimit)
			spec.CertificateName, spec.MatchExpression, spec.DeviceLabels = "credential", "true", map[string]string{}
			policy := EnrollmentApprovalPolicy{Metadata: ObjectMeta{Name: lo.ToPtr("west")}, Spec: spec}
			if tc.semanticError {
				require.NotEmpty(t, policy.Validate())
			} else {
				require.Empty(t, policy.Validate())
			}
			encoded, err := json.Marshal(spec)
			require.NoError(t, err)
			var roundtrip EnrollmentApprovalPolicySpec
			require.NoError(t, json.Unmarshal(encoded, &roundtrip))
			require.Equal(t, spec.ApprovalLimit, roundtrip.ApprovalLimit)
		})
	}
}

func TestPolicyPatchAllowsNullableValues(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	shared := spec.Components.Schemas["PatchRequest"].Value.Items.Value.Properties["value"].Value
	policy := spec.Paths.Find("/enrollmentapprovalpolicies/{name}").Patch.RequestBody.Value.Content["application/json-patch+json"].Schema.Value.Items.Value.Properties["value"].Value
	require.False(t, shared.Nullable)
	require.True(t, policy.Nullable)
}

func TestEnrollmentApprovalPolicyOperationResponses(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	post := spec.Paths.Find("/enrollmentapprovalpolicies").Post.Responses
	require.Nil(t, post.Value("404"))
	require.NotNil(t, post.Value("400"))
	require.NotNil(t, post.Value("500"))
	policy := spec.Paths.Find("/enrollmentapprovalpolicies/{name}")
	require.Nil(t, policy.Put.Responses.Value("201"))
	require.NotNil(t, policy.Put.Responses.Value("404"))
	require.NotNil(t, policy.Put.Responses.Value("500"))
	require.NotNil(t, policy.Patch.Responses.Value("500"))
	require.NotNil(t, policy.Delete.Responses.Value("409"))
	require.NotNil(t, policy.Delete.Responses.Value("500"))
	require.NotNil(t, spec.Paths.Find("/certificatesigningrequests/{name}").Delete.Responses.Value("409"))
}
