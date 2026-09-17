package common

import (
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/assert"
)

func TestBuildReportedOrganizations(t *testing.T) {
	tests := []struct {
		name          string
		organizations []string
		orgRoles      map[string][]string
		expectedOrgs  []string
		expectedRoles map[string][]string
	}{
		{
			name:          "When an organization is reported twice it should appear once with its roles",
			organizations: []string{"cz", "cz", "sk"},
			orgRoles: map[string][]string{
				"cz": {v1beta1.ExternalRoleInstaller, v1beta1.ExternalRoleOperator},
				"sk": {v1beta1.ExternalRoleViewer},
			},
			expectedOrgs: []string{"cz", "sk"},
			expectedRoles: map[string][]string{
				"cz": {v1beta1.ExternalRoleInstaller, v1beta1.ExternalRoleOperator},
				"sk": {v1beta1.ExternalRoleViewer},
			},
		},
		{
			name:          "When duplicates are collapsed it should preserve first-seen order",
			organizations: []string{"sk", "cz", "sk"},
			orgRoles: map[string][]string{
				"cz": {v1beta1.ExternalRoleOperator},
				"sk": {v1beta1.ExternalRoleViewer},
			},
			expectedOrgs: []string{"sk", "cz"},
			expectedRoles: map[string][]string{
				"cz": {v1beta1.ExternalRoleOperator},
				"sk": {v1beta1.ExternalRoleViewer},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := BuildReportedOrganizations(tt.organizations, tt.orgRoles, false)

			names := make([]string, 0, len(got))
			for _, org := range got {
				names = append(names, org.Name)
				assert.Equal(t, tt.expectedRoles[org.Name], org.Roles, "roles for organization %q", org.Name)
			}
			assert.Equal(t, tt.expectedOrgs, names)
		})
	}
}
