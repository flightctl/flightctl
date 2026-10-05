package store

// Catalog item references can appear in three places inside a fleet's spec: the
// OS image, an application image, and an application volume image. The JSONB
// accessors below locate them and are shared by the fleet-spec expression
// indexes and by every query that probes them, so that a change to one cannot
// silently stop matching the other.
const (
	// FleetSpecOsCatalogRefJSONB is the JSONB accessor, relative to a fleet row's
	// spec column, for the OS image catalog item reference object. Append
	// ->>'catalog' or ->>'item' to read a field of the reference.
	FleetSpecOsCatalogRefJSONB = `->'template'->'spec'->'os'->'catalogItemRef'`

	// FleetSpecAppCatalogRefPath is the jsonpath, relative to a fleet row's spec
	// column, matching every application image catalog item reference.
	FleetSpecAppCatalogRefPath = `$.template.spec.applications[*].catalogItemRef`

	// FleetSpecVolumeCatalogRefPath is the jsonpath, relative to a fleet row's
	// spec column, matching every application volume image catalog item reference.
	FleetSpecVolumeCatalogRefPath = `$.template.spec.applications[*].volumes[*].image.catalogItemRef`
)
