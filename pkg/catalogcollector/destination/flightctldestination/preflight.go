package flightctldestination

import (
	"context"
	"fmt"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
)

// preflightListLimit bounds each preflight list request to a single item. The
// check only has to prove that the collection can be read; fetching more would
// make startup cost scale with the size of the target's catalog.
const preflightListLimit int32 = 1

var _ catalogcollector.DestinationPreflight = (*reconciler)(nil)

// Preflight validates that the configured Flightctl API server is reachable
// with the configured client and that both collections this destination
// reconciles can be read.
//
// It issues one bounded, read-only list request against Catalogs and one
// against CatalogItems using the same client as Reconcile, so the configured
// authentication, TLS, organization, and API version all take part in the
// check. No label selector is applied: a destination is shared by pipelines
// and the per-pipeline ownership selector is not known at this point, so the
// check validates access to the collection itself.
//
// An empty collection is a successful result. A fresh deployment has nothing
// to list, and the destination's own first reconciliation is what populates
// it.
//
// A 200 response whose body carries no items field is rejected. The generated
// client decodes any JSON body on a 200 into the list type, so bodies such as
// {}, null, or an unrelated object would otherwise decode into a list with a
// nil Items and pass as if the collection had been read. An empty list sends
// "items": [], which decodes to a non-nil empty slice and is accepted.
//
// Preflight never writes. A successful result therefore proves reachability,
// authentication, and read authorization only; it does not prove that the
// create, update, and delete operations Reconcile performs are permitted.
func (c *reconciler) Preflight(ctx context.Context) error {
	limit := preflightListLimit

	catalogsResp, err := c.client.ListCatalogsWithResponse(
		ctx,
		&apiv1alpha1.ListCatalogsParams{Limit: &limit},
	)
	if err != nil {
		return fmt.Errorf(
			"destination %q: preflight: listing catalogs: %w",
			c.id,
			err,
		)
	}
	if catalogsResp == nil {
		return fmt.Errorf(
			"destination %q: preflight: LIST catalogs request returned no response",
			c.id,
		)
	}
	if catalogsResp.JSON200 == nil {
		return fmt.Errorf(
			"destination %q: preflight: listing catalogs: %w",
			c.id,
			apiError("LIST catalogs", catalogsResp.HTTPResponse, catalogsResp.Body),
		)
	}
	if catalogsResp.JSON200.Items == nil {
		return fmt.Errorf(
			"destination %q: preflight: listing catalogs: "+
				"response missing required items field",
			c.id,
		)
	}

	itemsResp, err := c.client.ListAllCatalogItemsWithResponse(
		ctx,
		&apiv1alpha1.ListAllCatalogItemsParams{Limit: &limit},
	)
	if err != nil {
		return fmt.Errorf(
			"destination %q: preflight: listing catalog items: %w",
			c.id,
			err,
		)
	}
	if itemsResp == nil {
		return fmt.Errorf(
			"destination %q: preflight: LIST catalog items request returned no response",
			c.id,
		)
	}
	if itemsResp.JSON200 == nil {
		return fmt.Errorf(
			"destination %q: preflight: listing catalog items: %w",
			c.id,
			apiError("LIST catalog items", itemsResp.HTTPResponse, itemsResp.Body),
		)
	}
	if itemsResp.JSON200.Items == nil {
		return fmt.Errorf(
			"destination %q: preflight: listing catalog items: "+
				"response missing required items field",
			c.id,
		)
	}

	return nil
}
