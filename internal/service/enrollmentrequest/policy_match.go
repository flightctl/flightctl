package enrollmentrequest

import (
	"context"
	"errors"
	"sort"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

var errPolicyMatchingUnavailable = errors.New("policy matching is unavailable")

type PolicyMatcher interface {
	Match(context.Context, domain.EnrollmentApprovalPolicy, domain.EnrollmentRequest) (bool, error)
}

type policyMatcher = PolicyMatcher

func WithPolicyMatcher(matcher PolicyMatcher) func(*ServiceHandler) {
	return func(handler *ServiceHandler) { handler.matcher = matcher }
}

type unavailablePolicyMatcher struct{}

func (unavailablePolicyMatcher) Match(context.Context, domain.EnrollmentApprovalPolicy, domain.EnrollmentRequest) (bool, error) {
	return false, errPolicyMatchingUnavailable
}

func selectEnrollmentPolicy(ctx context.Context, matcher policyMatcher, policies []domain.EnrollmentApprovalPolicy, request domain.EnrollmentRequest, eligible func(domain.EnrollmentApprovalPolicy) string) (*domain.EnrollmentApprovalPolicy, string) {
	if matcher == nil {
		matcher = unavailablePolicyMatcher{}
	}
	candidates := append([]domain.EnrollmentApprovalPolicy(nil), policies...)
	sort.Slice(candidates, func(left, right int) bool {
		return lo.FromPtr(candidates[left].Metadata.Name) < lo.FromPtr(candidates[right].Metadata.Name)
	})
	var selected *domain.EnrollmentApprovalPolicy
	matches := 0
	reason := "NoMatchingPolicy"
	for _, policy := range candidates {
		if !policy.Spec.Enabled || request.Status == nil || policy.Spec.CertificateName != lo.FromPtr(request.Status.EnrollmentCertificateName) {
			continue
		}
		matched, err := matcher.Match(ctx, policy, request)
		if err != nil {
			if errors.Is(err, errPolicyMatchingUnavailable) {
				reason = "PolicyMatchingUnavailable"
			} else if reason != "PolicyMatchingUnavailable" {
				reason = "PolicyEvaluationFailed"
			}
			continue
		}
		if matched {
			if eligible != nil {
				if failure := eligible(policy); failure != "" {
					reason = failure
					continue
				}
			}
			matches++
			selected = &policy
		}
	}
	if matches > 1 {
		return nil, "AmbiguousPolicyMatch"
	}
	if matches == 1 {
		return selected, ""
	}
	return nil, reason
}
