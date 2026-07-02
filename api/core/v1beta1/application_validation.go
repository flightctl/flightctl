package v1beta1

import (
	"fmt"
	"maps"
	"slices"

	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/internal/util/validation"
	"sigs.k8s.io/yaml"
)

type applicationValidator interface {
	// ValidateContents returns any errors for the supplied individual contents. It is expected for a caller
	// to validate the contents before validating the application so that the validator can build state.
	// When fleetTemplate is true, template expressions like {{ .metadata.labels.x }} are allowed in image references.
	ValidateContents(path string, contents []byte, fleetTemplate bool) []error
	// Validate returns any errors for the application evaluated as a whole.
	Validate() []error
}

type composeValidator struct {
	paths map[string]struct{}
}

// fleets require additional processing to ensure templates are defined appropriately
func specImageValidationFn(fleetTemplate bool) validation.ImageValidationFn {
	return func(imageRef *string, path string) []error {
		containsParams, paramErrs := validateParametersInString(imageRef, path, fleetTemplate)
		allErrs := append([]error{}, paramErrs...)

		if !containsParams {
			allErrs = append(allErrs, validation.ValidateOciImageReferenceStrict(imageRef, path)...)
		} else {
			allErrs = append(allErrs, validation.ValidateOciImageReferenceWithTemplates(imageRef, path)...)
		}
		return allErrs
	}
}

func (c *composeValidator) ValidateContents(path string, content []byte, fleetTemplate bool) []error {
	c.paths[path] = struct{}{}
	composeSpec, err := common.ParseComposeSpec(content)
	if err != nil {
		return []error{fmt.Errorf("parse compose spec: %w", err)}
	}
	return validation.ValidateComposeSpec(composeSpec, validation.WithSpecImageValidator(specImageValidationFn(fleetTemplate)))
}
func (c *composeValidator) Validate() []error {
	if err := validation.ValidateComposePaths(slices.Collect(maps.Keys(c.paths))); err != nil {
		return []error{fmt.Errorf("spec.applications[].inline[].path: %w", err)}
	}
	return nil
}

type quadletValidator struct {
	quadlets map[string]*common.QuadletReferences
}

func (q *quadletValidator) ValidateContents(path string, content []byte, fleetTemplate bool) []error {
	// Quadlet apps can come with misc files, so only validate that the quadlet files are defined correctly
	if quadlet.IsQuadletFile(path) {
		quadletSpec, err := common.ParseQuadletReferences(content)
		if err != nil {
			return []error{fmt.Errorf("parse quadlet spec %q: %w", path, err)}
		}
		q.quadlets[path] = quadletSpec

		return validation.ValidateQuadletSpec(quadletSpec, path, validation.WithSpecImageValidator(specImageValidationFn(fleetTemplate)))
	}
	return nil
}

func (q *quadletValidator) Validate() []error {
	var errs []error
	if err := validation.ValidateQuadletPaths(slices.Collect(maps.Keys(q.quadlets))); err != nil {
		errs = append(errs, fmt.Errorf("spec.applications[].inline[].path: %w", err))
	}
	errs = append(errs, validation.ValidateQuadletCrossReferences(q.quadlets)...)
	errs = append(errs, validation.ValidateQuadletNames(q.quadlets)...)
	return errs
}

type unknownAppTypeValidator struct {
	appType AppType
}

func (u *unknownAppTypeValidator) ValidateContents(path string, content []byte, fleetTemplate bool) []error {
	return nil
}

func (u *unknownAppTypeValidator) Validate() []error {
	return []error{fmt.Errorf("unsupported application type: %s", u.appType)}
}

// vmValidator validates the inline package contents for a VmApplication.
// Rules: exactly one vm.yaml required; vm.yaml must have kind VirtualMachine,
// apiVersion kubevirt.io/v1, and metadata.name matching the application name;
// no other files are allowed.
type vmValidator struct {
	appName   string
	hasVmYaml bool
}

type vmManifestMeta struct {
	ApiVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

func (v *vmValidator) ValidateContents(path string, content []byte, fleetTemplate bool) []error {
	if path == "vm.yaml" {
		v.hasVmYaml = true
		var meta vmManifestMeta
		if err := yaml.Unmarshal(content, &meta); err != nil {
			return []error{fmt.Errorf("spec.applications[%s].inline[vm.yaml]: must be valid YAML: %w", v.appName, err)}
		}
		var errs []error
		if meta.Kind != "VirtualMachine" {
			errs = append(errs, fmt.Errorf("spec.applications[%s].inline[vm.yaml]: kind must be \"VirtualMachine\", got %q", v.appName, meta.Kind))
		}
		if meta.ApiVersion != "kubevirt.io/v1" {
			errs = append(errs, fmt.Errorf("spec.applications[%s].inline[vm.yaml]: apiVersion must be \"kubevirt.io/v1\", got %q", v.appName, meta.ApiVersion))
		}
		if meta.Metadata.Name != v.appName {
			errs = append(errs, fmt.Errorf("spec.applications[%s].inline[vm.yaml]: metadata.name %q must match application name %q", v.appName, meta.Metadata.Name, v.appName))
		}
		return errs
	}
	return []error{fmt.Errorf("spec.applications[%s].inline: unrecognised file %q; only vm.yaml is allowed", v.appName, path)}
}

func (v *vmValidator) Validate() []error {
	if !v.hasVmYaml {
		return []error{fmt.Errorf("spec.applications[%s].inline: must contain exactly one file named \"vm.yaml\"", v.appName)}
	}
	return nil
}
