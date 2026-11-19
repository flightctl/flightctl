package tasks

import (
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/util"
)

// fleetMigrationDisposition is what the migration will do with one fleet.
type fleetMigrationDisposition int

const (
	// dispositionTransform means the fleet's OS image should be guarded.
	dispositionTransform fleetMigrationDisposition = iota
	// dispositionSkipNoImage means the fleet names no OS image to guard. A fleet
	// that selects its OS through a catalog item reference lands here, because
	// the image string is what the guard wraps.
	dispositionSkipNoImage
	// dispositionSkipConditionalImage means the image is already conditional, so
	// wrapping it again would change nothing an operator wants and would nest a
	// conditional the API does not accept.
	dispositionSkipConditionalImage
	// dispositionDeferResourceSync means a repository is the source of truth for
	// this fleet, so the change has to be made there instead.
	dispositionDeferResourceSync
)

// classifyFleetForMigration decides what to do with one fleet, returning the
// owning resource's identity when the fleet is deferred.
//
// The image predicates run before the ownership check so that a repository-
// backed fleet with nothing to migrate is not reported to its owner as work.
func classifyFleetForMigration(fleet *domain.Fleet) (fleetMigrationDisposition, string) {
	os := fleet.Spec.Template.Spec.Os
	if os == nil || strings.TrimSpace(os.Image) == "" {
		return dispositionSkipNoImage, ""
	}
	if !osImageEligibleForGuard(os.Image) {
		return dispositionSkipConditionalImage, ""
	}
	if owner, ok := resourceSyncOwner(fleet); ok {
		return dispositionDeferResourceSync, owner
	}
	return dispositionTransform, ""
}

// resourceSyncOwner reports whether a fleet is owned by a resource sync, and by
// which one. Ownership is the "kind/name" owner string the sync reconciler
// writes, which is this repository's ownership model.
func resourceSyncOwner(fleet *domain.Fleet) (string, bool) {
	if fleet.Metadata.Owner == nil {
		return "", false
	}
	kind, name, err := util.GetResourceOwner(fleet.Metadata.Owner)
	if err != nil {
		return "", false
	}
	if kind != domain.ResourceSyncKind {
		return "", false
	}
	return domain.ResourceSyncKind + "/" + name, true
}

// osImageEligibleForGuard reports whether a fleet template OS image can be
// wrapped in a conditional.
//
// Only a value made of literal text and simple substitutions can be wrapped. A
// value that already contains template control flow is left alone, whether the
// condition has anything to do with OS mode or not: wrapping it would nest one
// conditional inside another, which the fleet API rejects, and an operator who
// wrote a condition already decided which devices get the image. A value that
// does not parse is left alone for the same reason — nothing can be concluded
// about it, and wrapping it could only make it worse.
//
// Substitutions themselves stay eligible, so a parameterized tag is still
// guarded.
func osImageEligibleForGuard(image string) bool {
	if strings.TrimSpace(image) == "" {
		return false
	}
	parsed, err := template.New("osimage").Option("missingkey=zero").Funcs(domain.GetGoTemplateFuncMap()).Parse(image)
	if err != nil {
		return false
	}
	if parsed.Root == nil {
		return false
	}
	for _, node := range parsed.Root.Nodes {
		switch node.(type) {
		case *parse.TextNode, *parse.ActionNode:
			// Literal text and a substitution are both safe to wrap.
		default:
			// Anything else is control flow: an if with or without an else, a
			// range, a with, or a nested template.
			return false
		}
	}
	return true
}

// guardedOsImage wraps a fleet template OS image so that it renders only for a
// device that can apply an OS image, and renders empty otherwise.
//
// The original value is preserved byte for byte inside the guard, so an
// existing substitution keeps working and nothing but the wrapper is added.
func guardedOsImage(image string) string {
	return "{{ if " + domain.DeviceHasImageModeFunc + " }}" + image + "{{ end }}"
}
