package labelsyncmapping

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
)

type celExpressionValidator struct {
	evaluator Evaluator
}

// NewCELExpressionValidator adapts CEL expression checks to LabelSyncMapping admission.
func NewCELExpressionValidator(evaluator Evaluator) (ExpressionValidator, error) {
	if evaluator == nil {
		return nil, errors.New("CEL evaluator is required")
	}
	return celExpressionValidator{evaluator: evaluator}, nil
}

func (v celExpressionValidator) ValidateLabelSyncMapping(_ context.Context, mapping domain.LabelSyncMapping) error {
	expectedKind := ResultKindMap
	if mapping.Spec.Key != nil {
		expectedKind = ResultKindScalar
	}
	return v.evaluator.ValidateExpressionIs(mapping.Spec.Expression, expectedKind)
}
