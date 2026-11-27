package enrollmentapprovalpolicy

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/enrollmentpolicy"
)

var ErrValidationUnavailable = errors.New("restricted policy validation is unavailable")

// ExpressionValidator validates a single policy expression. Invalid expressions
// return InvalidPolicyError and an unavailable compiler returns ErrValidationUnavailable.
type ExpressionValidator interface {
	ValidateExpression(context.Context, string) error
}

// CELExpressionValidator checks expressions in the enrollment matcher environment.
type CELExpressionValidator struct {
	matcher *enrollmentpolicy.Matcher
}

func NewCELExpressionValidator() (*CELExpressionValidator, error) {
	matcher, err := enrollmentpolicy.NewMatcher()
	if err != nil {
		return nil, err
	}
	return &CELExpressionValidator{matcher: matcher}, nil
}

func (validator *CELExpressionValidator) ValidateExpression(_ context.Context, expression string) error {
	if _, err := validator.matcher.Compile(expression); err != nil {
		return InvalidPolicyError{Message: err.Error(), Cause: err}
	}
	return nil
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
