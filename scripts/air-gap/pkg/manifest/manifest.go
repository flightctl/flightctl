// Package manifest defines the schema for the build-time resolved mirror manifest
// that is embedded in the flightctl-mirror-images binary.
//
// The manifest is produced once at build time by "make generate-mirror-embed"
// and captures the fully-resolved image and RPM lists for every supported variant.
// The runtime tool reads it without any YAML parsing; --tag-override and the
// MIRROR_MANIFEST env-var override remain as escape hatches.
package manifest

// CurrentSchemaVersion identifies the manifest format.  Increment when the
// schema changes in a backward-incompatible way.
//
// Version 2 added Variant.OptionalImages.  A version 1 manifest supplied via
// MIRROR_MANIFEST is rejected rather than silently mirrored without its
// optional groups, so an operator never ends up with a bundle that is missing
// images they asked for.
const CurrentSchemaVersion = "2"

// OptionalGroupCatalogCollector is the opt-in group holding the catalog
// collector image.  The collector is an add-on: most deployments never
// install flightctl-catalog-collector, and mirroring its image by default
// would add weight to every air-gapped bundle for a component that is not
// going to be run.
const OptionalGroupCatalogCollector = "catalog-collector"

// Build is the top-level manifest structure embedded in the binary.
type Build struct {
	// SchemaVersion allows the runtime to detect format mismatches.
	SchemaVersion string `json:"schema_version"`

	// AppVersion is the chart appVersion (e.g. "1.2.0") read from Chart.yaml
	// at build time.  Used as the default image tag when --tag-override is not set.
	AppVersion string `json:"app_version"`

	// Variants maps each supported variant name to its fully-resolved data.
	Variants map[string]Variant `json:"variants"`
}

// Variant holds the pre-resolved image and RPM data for one chart variant.
type Variant struct {
	// Images is the deduplicated, sorted list of images mirrored by default
	// for this variant.
	Images []Image `json:"images"`

	// OptionalImages holds image groups that are mirrored only when the
	// caller opts in with --include-optional.  Each key is a group name (see
	// OptionalGroupCatalogCollector); the values are resolved exactly like
	// Images.  Groups exist for add-on components that most installations do
	// not deploy, so their images do not inflate every bundle.
	OptionalImages map[string][]Image `json:"optional_images,omitempty"`

	// RPMs is the sorted list of runtime RPM package names parsed from flightctl.spec.
	RPMs []string `json:"rpms"`
}

// Image is one container image in the pre-resolved manifest.
type Image struct {
	// Ref is the fully-qualified, registry-normalized image reference without a
	// tag (e.g. "registry.redhat.io/rhem/flightctl-api-rhel9").
	Ref string `json:"ref"`

	// Tag is the fixed image tag.  When non-empty it is always used as-is,
	// even if --tag-override is set (these are third-party images with pinned
	// versions).  When empty the effective version tag (AppVersion or
	// --tag-override) is applied at runtime.
	Tag string `json:"tag"`
}
