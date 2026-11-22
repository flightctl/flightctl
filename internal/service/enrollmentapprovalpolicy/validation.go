package enrollmentapprovalpolicy

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
)

var ErrValidationUnavailable = errors.New("restricted policy validation is unavailable")

type PolicySetValidator interface {
	ValidatePolicySet(context.Context, *domain.EnrollmentApprovalPolicy, []domain.EnrollmentApprovalPolicy) error
}

type UnavailablePolicySetValidator struct{}

func (UnavailablePolicySetValidator) ValidatePolicySet(context.Context, *domain.EnrollmentApprovalPolicy, []domain.EnrollmentApprovalPolicy) error {
	return ErrValidationUnavailable
}

type InvalidPolicyError struct {
	Message string
	Cause   error
}

func (failure InvalidPolicyError) Unwrap() error { return failure.Cause }

type InvalidPolicyInputError struct{ Cause error }

func (failure InvalidPolicyInputError) Error() string { return failure.Cause.Error() }
func (failure InvalidPolicyInputError) Unwrap() error { return failure.Cause }

func (failure InvalidPolicyError) Error() string { return failure.Message }

type PolicyOverlapError struct{ PolicyName string }

func (failure PolicyOverlapError) Error() string {
	return "enabled policy overlaps or cannot be proved disjoint from policy " + failure.PolicyName
}
