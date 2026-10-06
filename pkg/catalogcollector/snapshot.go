package catalogcollector

import (
	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
)

// CatalogSnapshot represents the complete desired state for a configured source
// scope as observed at a point in time.
//
// Every emitted snapshot must be complete for the configured scope. If fetching,
// pagination, or normalization fails, the source must report the error and must
// not deliver a partial desired state to the downstream consumer.
//
// Revision is an opaque, deterministic identifier that changes only when the
// desired source content changes. Destinations may use it for optimization but
// must not treat an unchanged revision as proof that their actual state is still
// correct. A source may replay a snapshot with the same revision so destinations
// can detect and repair drift.
//
// Consumers must treat the supplied snapshot and its slices as immutable.
// Anything retaining or transforming the data must copy what it needs.
// The caller retains ownership of the supplied snapshot.
type CatalogSnapshot struct {
	// Revision is an opaque, deterministic revision of the source-produced
	// desired content. It changes only when desired content changes.
	Revision string

	// Catalogs contains the desired Catalog resources.
	Catalogs []apiv1alpha1.Catalog

	// CatalogItems contains the desired CatalogItem resources.
	CatalogItems []apiv1alpha1.CatalogItem
}
