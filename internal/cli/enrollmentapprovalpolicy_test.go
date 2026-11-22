package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnrollmentApprovalPolicyResourceKind(t *testing.T) {
	for _, name := range []string{"EnrollmentApprovalPolicy", "enrollmentapprovalpolicy", "enrollmentapprovalpolicies"} {
		t.Run("When the resource name is "+name+" it should recognize the policy kind", func(t *testing.T) {
			kind, err := ResourceKindFromString(name)
			require.NoError(t, err)
			require.Equal(t, "enrollmentapprovalpolicies", kind.ToPlural())
		})
	}
}
