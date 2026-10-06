package helm

import (
	"os"
	"strings"
	"testing"
)

func TestHelmDocumentation_WhenSoftwareCatalogProcedureIsChecked_ItShouldIncludeOnboardingAndAccessGuidance(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{
			name: "the README template",
			path: "flightctl/README.md.gotmpl",
		},
		{
			name: "the generated README",
			path: "flightctl/README.md",
		},
	}

	requiredContent := []string{
		"Install from the OpenShift Software Catalog",
		"Create Project",
		"enter `flightctl` as the project name",
		"Ecosystem > Software Catalog",
		"flightcontrol",
		"flightctl-ui",
		"Copy login command",
	}

	for _, tt := range tests {
		t.Run("When checking "+tt.name+" it should include the Software Catalog procedure", func(t *testing.T) {
			content, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}

			for _, required := range requiredContent {
				if !strings.Contains(string(content), required) {
					t.Errorf("%s does not contain %q", tt.path, required)
				}
			}
		})
	}
}
