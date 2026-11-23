package enrollmentapprovalpolicy

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
)

var ErrValidationUnavailable = errors.New("restricted policy validation is unavailable")

// ExpressionValidator validates a single policy expression. Invalid expressions
// return InvalidPolicyError and an unavailable compiler returns ErrValidationUnavailable.
type ExpressionValidator interface {
	ValidateExpression(context.Context, string) error
}

type UnavailableExpressionValidator struct{}

func (UnavailableExpressionValidator) ValidateExpression(context.Context, string) error {
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

type approvalLimitAuthorizationError struct{ cause error }

func (failure approvalLimitAuthorizationError) Error() string { return failure.status().Message }
func (failure approvalLimitAuthorizationError) Unwrap() error { return failure.cause }

func (failure approvalLimitAuthorizationError) status() domain.Status {
	if failure.cause == nil {
		return domain.StatusForbidden(consts.ApprovalLimitPermissionRequiredMessage)
	}
	if flterrors.IsClientAuthError(failure.cause) {
		return domain.StatusBadRequest("Unable to verify request")
	}
	return domain.StatusServiceUnavailable("Authorization server unavailable")
}

func CheckApprovalLimitPermission(ctx context.Context, authorize ApprovalLimitAuthorizer) domain.Status {
	allowed, err := authorize(ctx)
	if err != nil || !allowed {
		return (approvalLimitAuthorizationError{cause: err}).status()
	}
	return domain.StatusOK()
}
