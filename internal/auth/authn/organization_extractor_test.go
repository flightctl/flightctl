package authn

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
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
