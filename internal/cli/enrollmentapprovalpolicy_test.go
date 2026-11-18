package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnrollmentApprovalPolicyResourceKind(t *testing.T) {
	for _, name := range []string{"EnrollmentApprovalPolicy", "enrollmentapprovalpolicy", "enrollmentapprovalpolicies"} {
		t.Run(name, func(t *testing.T) {
			kind, err := ResourceKindFromString(name)
			require.NoError(t, err)
			require.Equal(t, "enrollmentapprovalpolicies", kind.ToPlural())
		})
	}
}
