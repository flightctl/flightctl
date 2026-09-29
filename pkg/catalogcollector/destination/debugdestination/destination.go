// Package debugdestination provides a diagnostic catalog collector destination
// that logs snapshot summaries instead of reconciling resources against a
// remote system.
//
// The destination is terminal and stateless. It emits structured log output at
// Info level and never mutates the supplied snapshot, reconciles API resources,
// adds management labels, performs pruning, or retains snapshot data.
//
// Verbosity controls how much detail is logged:
//   - basic: pipeline ID, revision, catalog count, and catalog-item count
//   - normal: basic fields plus sorted catalog names and catalog-item identities
//   - detailed: basic summary followed by one deterministic log entry for every
//     Catalog and CatalogItem, including its JSON representation
//
// Detailed mode should be enabled deliberately because it logs all catalog
// resource data and may expose operational metadata. The destination does not
// receive component configuration, authentication extensions, bearer tokens,
// OAuth credentials, or HTTP headers.
package debugdestination

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
)

// destination is the debug destination implementation.
//
// It is safe for concurrent use because Reconcile does not mutate shared state.
// Each call derives local log entries from the immutable base entry.
type destination struct {
	log       *logrus.Entry
	verbosity Verbosity
}

// Reconcile logs the snapshot according to the configured verbosity.
func (d *destination) Reconcile(
	ctx context.Context,
	pipelineID string,
	snapshot *catalogcollector.CatalogSnapshot,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if snapshot == nil {
		return fmt.Errorf("debug destination received nil snapshot")
	}

	baseLog := d.log.WithFields(logrus.Fields{
		"pipeline_id": pipelineID,
		"revision":    snapshot.Revision,
	})

	summaryLog := baseLog.WithFields(logrus.Fields{
		"catalog_count":      len(snapshot.Catalogs),
		"catalog_item_count": len(snapshot.CatalogItems),
	})

	switch d.verbosity {
	case VerbosityBasic:
		summaryLog.Info("snapshot received")

	case VerbosityNormal:
		catalogNames, itemIdentities := resourceIdentities(snapshot)

		summaryLog.WithFields(logrus.Fields{
			"catalogs":      catalogNames,
			"catalog_items": itemIdentities,
		}).Info("snapshot received")

	case VerbosityDetailed:
		summaryLog.Info("snapshot received")

		if err := logDetailedResources(baseLog, snapshot); err != nil {
			return err
		}

	default:
		return fmt.Errorf(
			"debug destination has unsupported verbosity %q",
			d.verbosity,
		)
	}

	return nil
}

// resourceIdentities returns sorted resource identities without modifying the
// supplied snapshot.
func resourceIdentities(
	snapshot *catalogcollector.CatalogSnapshot,
) ([]string, []string) {
	catalogNames := make([]string, 0, len(snapshot.Catalogs))
	for i := range snapshot.Catalogs {
		catalogNames = append(
			catalogNames,
			nameOf(snapshot.Catalogs[i].Metadata.Name),
		)
	}
	sort.Strings(catalogNames)

	itemIdentities := make([]string, 0, len(snapshot.CatalogItems))
	for i := range snapshot.CatalogItems {
		item := &snapshot.CatalogItems[i]
		itemIdentities = append(
			itemIdentities,
			item.Metadata.Catalog+"/"+nameOf(item.Metadata.Name),
		)
	}
	sort.Strings(itemIdentities)

	return catalogNames, itemIdentities
}

// logDetailedResources logs each resource separately in deterministic order.
//
// Resources are serialized through their JSON representation rather than
// logged as Go structs. This prevents pointer addresses and Go map formatting
// from appearing in diagnostic output.
func logDetailedResources(
	log *logrus.Entry,
	snapshot *catalogcollector.CatalogSnapshot,
) error {
	catalogs := slices.Clone(snapshot.Catalogs)
	sort.SliceStable(catalogs, func(i, j int) bool {
		return nameOf(catalogs[i].Metadata.Name) <
			nameOf(catalogs[j].Metadata.Name)
	})

	for i := range catalogs {
		name := nameOf(catalogs[i].Metadata.Name)
		if err := logResource(
			log,
			"Catalog",
			"",
			name,
			catalogs[i],
		); err != nil {
			return err
		}
	}

	items := slices.Clone(snapshot.CatalogItems)
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i].Metadata.Catalog + "/" +
			nameOf(items[i].Metadata.Name)
		right := items[j].Metadata.Catalog + "/" +
			nameOf(items[j].Metadata.Name)
		return left < right
	})

	for i := range items {
		catalogName := items[i].Metadata.Catalog
		itemName := nameOf(items[i].Metadata.Name)

		if err := logResource(
			log,
			"CatalogItem",
			catalogName,
			itemName,
			items[i],
		); err != nil {
			return err
		}
	}

	return nil
}

// logResource writes one detailed resource entry using its JSON representation.
func logResource(
	log *logrus.Entry,
	kind string,
	catalogName string,
	resourceName string,
	resource any,
) error {
	data, err := json.Marshal(resource)
	if err != nil {
		return fmt.Errorf(
			"marshaling %s %q for debug output: %w",
			kind,
			resourceIdentity(catalogName, resourceName),
			err,
		)
	}

	fields := logrus.Fields{
		"resource_kind": kind,
		"resource_name": resourceName,
		"resource_json": string(data),
	}
	if catalogName != "" {
		fields["catalog_name"] = catalogName
	}

	log.WithFields(fields).Info("snapshot resource")
	return nil
}

func resourceIdentity(catalogName, resourceName string) string {
	if catalogName == "" {
		return resourceName
	}
	return catalogName + "/" + resourceName
}

func nameOf(name *string) string {
	if name == nil {
		return ""
	}
	return *name
}
