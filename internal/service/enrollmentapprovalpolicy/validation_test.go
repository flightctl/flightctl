package enrollmentapprovalpolicy_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	"github.com/stretchr/testify/require"
)

func TestUnavailableValidator(t *testing.T) {
	require.ErrorIs(t, policyservice.UnavailableExpressionValidator{}.ValidateExpression(t.Context(), "true"), policyservice.ErrValidationUnavailable)
}

func TestPolicyErrorCauses(t *testing.T) {
	cause := errors.New("cause")
	semantic := policyservice.InvalidPolicyError{Message: "invalid expression", Cause: cause}
	require.ErrorIs(t, semantic, cause)
	syntax := &json.SyntaxError{Offset: 12}
	input := policyservice.InvalidPolicyInputError{Cause: fmt.Errorf("apply patch: %w", syntax)}
	var recovered *json.SyntaxError
	require.ErrorAs(t, input, &recovered)
	require.Same(t, syntax, recovered)
}
