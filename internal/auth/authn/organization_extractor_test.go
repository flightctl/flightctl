package authn

import (
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyOrgPrefix(t *testing.T) {
	tests := []struct {
		name     string
		orgName  string
		prefix   *string
		expected string
	}{
		{"nil prefix returns org name unchanged", "my-org", nil, "my-org"},
		{"empty prefix returns org name unchanged", "my-org", lo.ToPtr(""), "my-org"},
		{"non-empty prefix prepends to org name", "my-org", lo.ToPtr("aap-"), "aap-my-org"},
		{"prefix with empty org name", "", lo.ToPtr("ocp-"), "ocp-"},
		{"k8s style prefix", "default", lo.ToPtr("k8s-"), "k8s-default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyOrgPrefix(tt.orgName, tt.prefix)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestExtractOrganizationsDynamic(t *testing.T) {
	tests := []struct {
		name     string
		claims   map[string]interface{}
		prefix   *string
		suffix   *string
		expected []string
	}{
		{
			name:     "When the org claim repeats a value it should be reported once",
			claims:   map[string]interface{}{"orginfo": []interface{}{"cz", "cz", "sk"}},
			expected: []string{"cz", "sk"},
		},
		{
			name:     "When duplicates are collapsed it should preserve first-seen order",
			claims:   map[string]interface{}{"orginfo": []interface{}{"sk", "cz", "sk"}},
			expected: []string{"sk", "cz"},
		},
		{
			name:     "When a prefix and suffix are configured duplicates should still collapse",
			claims:   map[string]interface{}{"orginfo": []interface{}{"cz", "cz"}},
			prefix:   lo.ToPtr("tsp-"),
			suffix:   lo.ToPtr("-org"),
			expected: []string{"tsp-cz-org"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var assignment api.AuthOrganizationAssignment
			require.NoError(t, assignment.FromAuthDynamicOrganizationAssignment(api.AuthDynamicOrganizationAssignment{
				Type:                   api.AuthDynamicOrganizationAssignmentTypeDynamic,
				ClaimPath:              []string{"orginfo"},
				OrganizationNamePrefix: tt.prefix,
				OrganizationNameSuffix: tt.suffix,
			}))

			extractor := NewOrganizationExtractor(&common.AuthOrganizationsConfig{OrganizationAssignment: &assignment})
			assert.Equal(t, tt.expected, extractor.ExtractOrganizations(tt.claims, "user@example.com"))
		})
	}
}
