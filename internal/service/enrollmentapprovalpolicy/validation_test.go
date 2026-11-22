package enrollmentapprovalpolicy_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	"github.com/stretchr/testify/require"
)

func TestUnavailableValidator(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "When draft is disabled it should fail closed", true: "When policy is enabled it should fail closed"}[enabled], func(t *testing.T) {
			candidate := &domain.EnrollmentApprovalPolicy{Spec: domain.EnrollmentApprovalPolicySpec{Enabled: enabled}}
			require.ErrorIs(t, policyservice.UnavailablePolicySetValidator{}.ValidatePolicySet(context.Background(), candidate, nil), policyservice.ErrValidationUnavailable)
		})
	}
	require.ErrorIs(t, policyservice.UnavailablePolicySetValidator{}.ValidatePolicySet(context.Background(), nil, nil), policyservice.ErrValidationUnavailable)
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
