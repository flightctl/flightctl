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
const CurrentSchemaVersion = "1"

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
	// Images is the deduplicated, sorted list of images for this variant.
	Images []Image `json:"images"`

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
