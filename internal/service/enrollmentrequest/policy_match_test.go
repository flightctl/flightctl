package enrollmentrequest

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type matchResult struct {
	matched bool
	err     error
}

type fakePolicyMatcher map[string]matchResult

func (matcher fakePolicyMatcher) Match(_ context.Context, policy domain.EnrollmentApprovalPolicy, _ domain.EnrollmentRequest) (bool, error) {
	result := matcher[lo.FromPtr(policy.Metadata.Name)]
	return result.matched, result.err
}

func TestSelectEnrollmentPolicy(t *testing.T) {
	policies := []domain.EnrollmentApprovalPolicy{
		{Metadata: domain.ObjectMeta{Name: lo.ToPtr("second")}, Spec: domain.EnrollmentApprovalPolicySpec{Enabled: true, CertificateName: "credential"}},
		{Metadata: domain.ObjectMeta{Name: lo.ToPtr("first")}, Spec: domain.EnrollmentApprovalPolicySpec{Enabled: true, CertificateName: "credential"}},
	}
	request := domain.EnrollmentRequest{Status: &domain.EnrollmentRequestStatus{EnrollmentCertificateName: lo.ToPtr("credential")}}
	for _, test := range []struct {
		name     string
		matcher  policyMatcher
		selected string
		reason   string
	}{
		{"When matching is unavailable it should remain pending", unavailablePolicyMatcher{}, "", "PolicyMatchingUnavailable"},
		{"When no policy matches it should remain pending", fakePolicyMatcher{}, "", "NoMatchingPolicy"},
		{"When one matches it should select it", fakePolicyMatcher{"first": {matched: true}}, "first", ""},
		{"When several match it should remain pending", fakePolicyMatcher{"first": {matched: true}, "second": {matched: true}}, "", "AmbiguousPolicyMatch"},
		{"When another candidate fails it should retain a passing candidate", fakePolicyMatcher{"first": {err: errors.New("sensitive")}, "second": {matched: true}}, "second", ""},
		{"When all candidates fail it should return a safe reason", fakePolicyMatcher{"first": {err: errors.New("sensitive")}}, "", "PolicyEvaluationFailed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, reason := selectEnrollmentPolicy(context.Background(), test.matcher, policies, request, nil)
			require.Equal(t, test.reason, reason)
			if test.selected == "" {
				require.Nil(t, selected)
			} else {
				require.Equal(t, test.selected, *selected.Metadata.Name)
			}
		})
	}
}
