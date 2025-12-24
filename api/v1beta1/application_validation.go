package v1beta1

import (
	"fmt"
	"maps"
	"slices"

	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/internal/util/validation"
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

func (c *composeValidator) ValidateContents(path string, content []byte, fleetTemplate bool) []error {
	c.paths[path] = struct{}{}
	composeSpec, err := common.ParseComposeSpec(content)
	if err != nil {
		return []error{fmt.Errorf("parse compose spec: %w", err)}
	}
	return validation.ValidateComposeSpec(composeSpec, fleetTemplate)
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
		return validation.ValidateQuadletSpec(quadletSpec, path, fleetTemplate)
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
