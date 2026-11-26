package enrollmentapprovalpolicy_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	"github.com/stretchr/testify/require"
)

func TestCELExpressionValidator(t *testing.T) {
	validator, err := policyservice.NewCELExpressionValidator()
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		expression string
		wantError  string
	}{
		{"When an expression uses reported identifiers it should validate", `systemInfo.customInfo["site"] in ["east", "west"]`, ""},
		{"When an expression is malformed it should report its parse error", `systemInfo.productSerial ==`, "parsing CEL expression"},
		{"When an expression returns a string it should report its output error", `systemInfo.productSerial`, "expression must return a boolean"},
		{"When an expression uses regex it should report its unsupported operator", `systemInfo.productSerial.matches("^west")`, "expected boolean literals or identifier comparisons"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validator.ValidateExpression(t.Context(), tc.expression)
			if tc.wantError == "" {
				require.NoError(t, err)
				return
			}
			var invalid policyservice.InvalidPolicyError
			require.ErrorAs(t, err, &invalid)
			require.ErrorContains(t, err, "spec.matchExpression")
			require.ErrorContains(t, err, tc.wantError)
			require.NotNil(t, invalid.Unwrap())
		})
	}
}

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
