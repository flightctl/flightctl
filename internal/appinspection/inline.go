package appinspection

import (
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/appspec"
)

// InspectCompose classifies images in the effective Compose services.
func InspectCompose(contents []v1beta1.ApplicationContent) Result {
	spec, err := appspec.ParsePartialComposeFromSpec(contents)
	var result Result
	if spec != nil {
		for _, service := range spec.Services {
			result.References = addReference(result.References, service.Image, RoleWorkload, ConsumerContainer)
		}
		result.References = uniqueReferences(result.References)
	}
	if err != nil {
		result.Diagnostics = []Diagnostic{DiagnosticParseFailed}
	}
	return result
}

// InspectQuadlet classifies images in merged units and their inline Pod YAML.
func InspectQuadlet(contents []v1beta1.ApplicationContent) Result {
	return quadletResult(appspec.ParsePartialQuadletImageReferencesFromSpec(contents))
}

func quadletResult(refs []appspec.QuadletImageReference, err error) Result {
	var result Result
	for _, ref := range refs {
		role := RoleWorkload
		if ref.IsVolume {
			role = RoleVolume
		}
		result.References = addReference(result.References, ref.Image, role, ConsumerContainer)
	}
	result.References = uniqueReferences(result.References)
	if err != nil {
		result.Diagnostics = []Diagnostic{DiagnosticParseFailed}
	}
	return result
}
