package enrollmentapprovalpolicy

import (
	"context"
	"errors"
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
