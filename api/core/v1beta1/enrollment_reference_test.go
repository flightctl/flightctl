package v1beta1

import (
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/stretchr/testify/require"
)

func TestEnrollmentCertificateReferenceName(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	property := spec.Components.Schemas["EnrollmentRequestStatus"].Value.Properties["enrollmentCertificateName"].Value
	require.True(t, property.ReadOnly)
	for _, testCase := range []struct {
		name  string
		value string
		valid bool
	}{
		{"When a name is one character it should be valid", "a", true},
		{"When a name is a subdomain it should be valid", "edge-1.example", true},
		{"When a label exceeds 63 characters it should follow the existing subdomain policy", strings.Repeat("a", 64), true},
		{"When a name reaches 253 characters it should be valid", strings.Repeat("a", 253), true},
		{"When a name exceeds 253 characters it should be invalid", strings.Repeat("a", 254), false},
		{"When a name is empty it should be invalid", "", false},
		{"When a name has uppercase letters it should be invalid", "Edge", false},
		{"When a name has an underscore it should be invalid", "edge_1", false},
		{"When a name has an empty label it should be invalid", "edge..example", false},
		{"When a name has a trailing hyphen it should be invalid", "edge-", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.valid, len(validation.ValidateResourceName(&testCase.value)) == 0)
			require.Equal(t, testCase.valid, property.VisitJSON(testCase.value) == nil)
		})
	}
}
