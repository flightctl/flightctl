// Package flightctldestination provides a catalog collector destination that
// mirrors complete CatalogSnapshot values into a Flightctl API server.
//
// A successful snapshot is authoritative for one pipeline: desired resources
// are created or updated, unchanged resources are left untouched, and resources
// previously managed by that pipeline but absent from the snapshot are deleted.
// Sources must never emit partial snapshots. An empty successful snapshot is a
// valid desired state and removes every resource managed by that pipeline.
//
// The destination marks resources with collector and pipeline labels. Those
// labels define the reconciliation and pruning boundary. Existing resources
// outside that boundary are never adopted or overwritten; a desired name that
// collides with such a resource causes reconciliation to fail.
//
// Pruning begins only after all desired writes succeed and complete, paginated
// lists of managed Catalogs and CatalogItems have been retrieved. CatalogItems
// are deleted before Catalogs. Resources managed by other pipelines or actors
// are never selected for pruning.
package flightctldestination

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	v1alpha1client "github.com/flightctl/flightctl/internal/api/client/v1alpha1"
	"github.com/flightctl/flightctl/internal/domain"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	managedByLabel = "flightctl.io/managed-by"
	managedByValue = "flightctl-catalog-collector"
	pipelineLabel  = "flightctl.io/catalog-collector-pipeline"

	listPageLimit int32 = 100

	maxAPIErrorBodyBytes = 4 * 1024
)

type reconciler struct {
	client *v1alpha1client.ClientWithResponses
	id     catalogcollector.ComponentID
	log    *logrus.Entry

	// The destination is shared by pipelines. Reconciliations for different
	// pipelines may run concurrently, but two snapshots for the same pipeline
	// must not interleave because either one may prune the other's desired state.
	pipelineLocks sync.Map // map[string]*sync.Mutex
}

type desiredState struct {
	catalogs     []apiv1alpha1.Catalog
	items        []apiv1alpha1.CatalogItem
	catalogNames map[string]struct{}
	itemKeys     map[catalogItemKey]struct{}
}

type catalogItemKey struct {
	catalog string
	name    string
}

// Reconcile makes the resources managed by pipelineID in Flightctl reflect the
// complete desired state in snapshot.
//
// The operation is not transactional, but destructive work is deliberately
// last: validation, desired writes, and every page of both managed-resource
// lists must succeed before any stale resource is deleted.
func (c *reconciler) Reconcile(
	ctx context.Context,
	pipelineID string,
	snapshot *catalogcollector.CatalogSnapshot,
) error {
	log := c.log.WithField("pipeline_id", pipelineID)

	if snapshot == nil {
		err := fmt.Errorf("snapshot must not be nil")
		log.WithError(err).Error("failed to prepare desired state")
		return fmt.Errorf("destination %q: %w", c.id, err)
	}

	log.WithFields(logrus.Fields{
		"revision":           snapshot.Revision,
		"catalog_count":      len(snapshot.Catalogs),
		"catalog_item_count": len(snapshot.CatalogItems),
	}).Debug("starting reconciliation")

	lock := c.pipelineLock(pipelineID)
	lock.Lock()
	defer lock.Unlock()

	desired, err := prepareDesiredState(pipelineID, snapshot)
	if err != nil {
		log.WithError(err).Error("failed to prepare desired state")
		return fmt.Errorf("destination %q: %w", c.id, err)
	}

	if err := c.applyDesiredState(ctx, pipelineID, desired); err != nil {
		log.WithError(err).Error("failed to apply desired state")
		return fmt.Errorf("destination %q: %w", c.id, err)
	}

	log.Debug("desired state applied, listing managed resources for pruning")

	// Fetch both complete collections before deleting anything. A failed or
	// partial list therefore cannot authorize pruning.
	actualItems, err := c.listManagedCatalogItems(ctx, pipelineID)
	if err != nil {
		log.WithError(err).Error("failed to list managed catalog items")
		return fmt.Errorf(
			"destination %q: listing managed catalog items: %w",
			c.id,
			err,
		)
	}

	actualCatalogs, err := c.listManagedCatalogs(ctx, pipelineID)
	if err != nil {
		log.WithError(err).Error("failed to list managed catalogs")
		return fmt.Errorf(
			"destination %q: listing managed catalogs: %w",
			c.id,
			err,
		)
	}

	if err := validateManagedResults(
		pipelineID,
		actualCatalogs,
		actualItems,
	); err != nil {
		log.WithError(err).Error("managed resource validation failed")
		return fmt.Errorf("destination %q: %w", c.id, err)
	}

	if err := c.pruneStaleState(
		ctx,
		desired,
		actualCatalogs,
		actualItems,
	); err != nil {
		log.WithError(err).Error("failed to prune stale resources")
		return fmt.Errorf("destination %q: %w", c.id, err)
	}

	staleCatalogCount := len(actualCatalogs) - len(desired.catalogNames)
	staleItemCount := len(actualItems) - len(desired.itemKeys)

	log.WithFields(logrus.Fields{
		"revision":                   snapshot.Revision,
		"desired_catalog_count":      len(desired.catalogNames),
		"desired_catalog_item_count": len(desired.itemKeys),
		"pruned_catalog_count":       max(0, staleCatalogCount),
		"pruned_catalog_item_count":  max(0, staleItemCount),
	}).Info("reconciliation complete")

	return nil
}

func (c *reconciler) pipelineLock(pipelineID string) *sync.Mutex {
	lock, _ := c.pipelineLocks.LoadOrStore(pipelineID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func prepareDesiredState(
	pipelineID string,
	snapshot *catalogcollector.CatalogSnapshot,
) (*desiredState, error) {
	if pipelineID == "" {
		return nil, fmt.Errorf("pipeline ID must not be empty")
	}
	if problems := validation.IsValidLabelValue(pipelineID); len(problems) > 0 {
		return nil, fmt.Errorf(
			"pipeline ID %q cannot be used as a label value: %s",
			pipelineID,
			strings.Join(problems, "; "),
		)
	}
	if snapshot == nil {
		return nil, fmt.Errorf("snapshot must not be nil")
	}

	desired := &desiredState{
		catalogs:     make([]apiv1alpha1.Catalog, 0, len(snapshot.Catalogs)),
		items:        make([]apiv1alpha1.CatalogItem, 0, len(snapshot.CatalogItems)),
		catalogNames: make(map[string]struct{}, len(snapshot.Catalogs)),
		itemKeys:     make(map[catalogItemKey]struct{}, len(snapshot.CatalogItems)),
	}

	for index := range snapshot.Catalogs {
		catalog := snapshot.Catalogs[index]
		name := nameOf(catalog.Metadata.Name)
		if name == "" {
			return nil, fmt.Errorf("catalog at index %d has no name", index)
		}
		if _, duplicate := desired.catalogNames[name]; duplicate {
			return nil, fmt.Errorf("snapshot contains duplicate catalog %q", name)
		}
		if err := addManagementLabels(
			&catalog.Metadata.Labels,
			pipelineID,
			"catalog",
			name,
		); err != nil {
			return nil, err
		}

		desired.catalogNames[name] = struct{}{}
		desired.catalogs = append(desired.catalogs, catalog)
	}

	for index := range snapshot.CatalogItems {
		item := snapshot.CatalogItems[index]
		catalogName := item.Metadata.Catalog
		itemName := nameOf(item.Metadata.Name)
		if itemName == "" {
			return nil, fmt.Errorf("catalog item at index %d has no name", index)
		}
		if catalogName == "" {
			return nil, fmt.Errorf("catalog item %q has no catalog", itemName)
		}

		key := catalogItemKey{catalog: catalogName, name: itemName}
		if _, duplicate := desired.itemKeys[key]; duplicate {
			return nil, fmt.Errorf(
				"snapshot contains duplicate catalog item %q/%q",
				catalogName,
				itemName,
			)
		}
		if err := addManagementLabels(
			&item.Metadata.Labels,
			pipelineID,
			"catalog item",
			catalogName+"/"+itemName,
		); err != nil {
			return nil, err
		}

		desired.itemKeys[key] = struct{}{}
		desired.items = append(desired.items, item)
	}

	sort.Slice(desired.catalogs, func(i, j int) bool {
		return nameOf(desired.catalogs[i].Metadata.Name) <
			nameOf(desired.catalogs[j].Metadata.Name)
	})
	sort.Slice(desired.items, func(i, j int) bool {
		left := catalogItemKey{
			catalog: desired.items[i].Metadata.Catalog,
			name:    nameOf(desired.items[i].Metadata.Name),
		}
		right := catalogItemKey{
			catalog: desired.items[j].Metadata.Catalog,
			name:    nameOf(desired.items[j].Metadata.Name),
		}
		return lessCatalogItemKey(left, right)
	})

	return desired, nil
}

// addManagementLabels copies the source labels and adds the labels reserved by
// the Flightctl destination. It never mutates the snapshot's original map.
func addManagementLabels(
	labels **map[string]string,
	pipelineID string,
	resourceKind string,
	resourceName string,
) error {
	if labels == nil {
		return fmt.Errorf(
			"%s %q has an invalid labels reference",
			resourceKind,
			resourceName,
		)
	}

	cloned := make(map[string]string)
	if *labels != nil {
		maps.Copy(cloned, **labels)
	}

	for _, reserved := range []string{managedByLabel, pipelineLabel} {
		if _, exists := cloned[reserved]; exists {
			return fmt.Errorf(
				"%s %q sets reserved label %q",
				resourceKind,
				resourceName,
				reserved,
			)
		}
	}

	cloned[managedByLabel] = managedByValue
	cloned[pipelineLabel] = pipelineID
	*labels = &cloned

	return nil
}

func (c *reconciler) applyDesiredState(
	ctx context.Context,
	pipelineID string,
	desired *desiredState,
) error {
	// Catalogs must exist before their CatalogItems can be written.
	for index := range desired.catalogs {
		catalog := desired.catalogs[index]
		name := nameOf(catalog.Metadata.Name)

		existing, err := c.getCatalog(ctx, name)
		if err != nil {
			return fmt.Errorf("reading catalog %q: %w", name, err)
		}

		if existing != nil {
			if err := requirePipelineOwnership(
				pipelineID,
				existing.Metadata.Labels,
				existing.Metadata.Owner,
				"catalog",
				name,
			); err != nil {
				return err
			}

			if domain.CatalogSpecsAreEqual(existing.Spec, catalog.Spec) &&
				labelsEqual(existing.Metadata.Labels, catalog.Metadata.Labels) {
				continue
			}
		}

		if err := c.replaceCatalog(ctx, name, catalog); err != nil {
			return fmt.Errorf("writing catalog %q: %w", name, err)
		}
	}

	for index := range desired.items {
		item := desired.items[index]
		catalogName := item.Metadata.Catalog
		itemName := nameOf(item.Metadata.Name)

		existing, err := c.getCatalogItem(ctx, catalogName, itemName)
		if err != nil {
			return fmt.Errorf(
				"reading catalog item %q/%q: %w",
				catalogName,
				itemName,
				err,
			)
		}

		if existing != nil {
			if err := requirePipelineOwnership(
				pipelineID,
				existing.Metadata.Labels,
				existing.Metadata.Owner,
				"catalog item",
				catalogName+"/"+itemName,
			); err != nil {
				return err
			}

			if domain.CatalogItemSpecsAreEqual(existing.Spec, item.Spec) &&
				labelsEqual(existing.Metadata.Labels, item.Metadata.Labels) {
				continue
			}
		}

		if err := c.replaceCatalogItem(ctx, catalogName, itemName, item); err != nil {
			return fmt.Errorf(
				"writing catalog item %q/%q: %w",
				catalogName,
				itemName,
				err,
			)
		}
	}

	return nil
}

func requirePipelineOwnership(
	pipelineID string,
	labels *map[string]string,
	owner *string,
	resourceKind string,
	resourceName string,
) error {
	if owner != nil && *owner != "" {
		return fmt.Errorf(
			"%s %q is owned by %q and cannot be managed by pipeline %q",
			resourceKind,
			resourceName,
			*owner,
			pipelineID,
		)
	}

	actual := labelsValue(labels)
	if actual[managedByLabel] != managedByValue ||
		actual[pipelineLabel] != pipelineID {
		return fmt.Errorf(
			"%s %q already exists outside pipeline %q management scope",
			resourceKind,
			resourceName,
			pipelineID,
		)
	}

	return nil
}

func validateManagedResults(
	pipelineID string,
	catalogs []apiv1alpha1.Catalog,
	items []apiv1alpha1.CatalogItem,
) error {
	for index := range catalogs {
		catalog := catalogs[index]
		name := nameOf(catalog.Metadata.Name)
		if name == "" {
			return fmt.Errorf("managed catalog list contains an unnamed catalog")
		}
		if err := requirePipelineOwnership(
			pipelineID,
			catalog.Metadata.Labels,
			catalog.Metadata.Owner,
			"catalog",
			name,
		); err != nil {
			return fmt.Errorf("invalid managed-resource list result: %w", err)
		}
	}

	for index := range items {
		item := items[index]
		catalogName := item.Metadata.Catalog
		itemName := nameOf(item.Metadata.Name)
		if catalogName == "" || itemName == "" {
			return fmt.Errorf(
				"managed catalog item list contains an item without catalog or name",
			)
		}
		if err := requirePipelineOwnership(
			pipelineID,
			item.Metadata.Labels,
			item.Metadata.Owner,
			"catalog item",
			catalogName+"/"+itemName,
		); err != nil {
			return fmt.Errorf("invalid managed-resource list result: %w", err)
		}
	}

	return nil
}

func (c *reconciler) pruneStaleState(
	ctx context.Context,
	desired *desiredState,
	actualCatalogs []apiv1alpha1.Catalog,
	actualItems []apiv1alpha1.CatalogItem,
) error {
	staleItems := make([]catalogItemKey, 0)
	for index := range actualItems {
		key := catalogItemKey{
			catalog: actualItems[index].Metadata.Catalog,
			name:    nameOf(actualItems[index].Metadata.Name),
		}
		if _, wanted := desired.itemKeys[key]; !wanted {
			staleItems = append(staleItems, key)
		}
	}
	sort.Slice(staleItems, func(i, j int) bool {
		return lessCatalogItemKey(staleItems[i], staleItems[j])
	})

	staleCatalogs := make([]string, 0)
	for index := range actualCatalogs {
		name := nameOf(actualCatalogs[index].Metadata.Name)
		if _, wanted := desired.catalogNames[name]; !wanted {
			staleCatalogs = append(staleCatalogs, name)
		}
	}
	sort.Strings(staleCatalogs)

	// Delete children first so stale managed items do not prevent deletion of
	// their containing catalogs.
	for _, key := range staleItems {
		if err := c.deleteCatalogItem(ctx, key.catalog, key.name); err != nil {
			return fmt.Errorf(
				"deleting stale catalog item %q/%q: %w",
				key.catalog,
				key.name,
				err,
			)
		}
	}

	for _, name := range staleCatalogs {
		if err := c.deleteCatalog(ctx, name); err != nil {
			return fmt.Errorf("deleting stale catalog %q: %w", name, err)
		}
	}

	return nil
}

func (c *reconciler) listManagedCatalogs(
	ctx context.Context,
	pipelineID string,
) ([]apiv1alpha1.Catalog, error) {
	selector := managementSelector(pipelineID)
	var continueToken *string
	seenTokens := make(map[string]struct{})
	var catalogs []apiv1alpha1.Catalog

	for {
		resp, err := c.client.ListCatalogsWithResponse(
			ctx,
			&apiv1alpha1.ListCatalogsParams{
				Continue:      continueToken,
				LabelSelector: &selector,
				Limit:         new(listPageLimit),
			},
		)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, fmt.Errorf("LIST catalogs request returned no response")
		}
		if resp.JSON200 == nil {
			return nil, apiError(
				"LIST catalogs",
				resp.HTTPResponse,
				resp.Body,
			)
		}

		catalogs = append(catalogs, resp.JSON200.Items...)
		continueToken = resp.JSON200.Metadata.Continue
		if continueToken == nil || *continueToken == "" {
			return catalogs, nil
		}
		if _, repeated := seenTokens[*continueToken]; repeated {
			return nil, fmt.Errorf(
				"server repeated pagination token while listing catalogs",
			)
		}
		seenTokens[*continueToken] = struct{}{}
	}
}

func (c *reconciler) listManagedCatalogItems(
	ctx context.Context,
	pipelineID string,
) ([]apiv1alpha1.CatalogItem, error) {
	selector := managementSelector(pipelineID)
	var continueToken *string
	seenTokens := make(map[string]struct{})
	var items []apiv1alpha1.CatalogItem

	for {
		resp, err := c.client.ListAllCatalogItemsWithResponse(
			ctx,
			&apiv1alpha1.ListAllCatalogItemsParams{
				Continue:      continueToken,
				LabelSelector: &selector,
				Limit:         new(listPageLimit),
			},
		)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, fmt.Errorf(
				"LIST catalog items request returned no response",
			)
		}
		if resp.JSON200 == nil {
			return nil, apiError(
				"LIST catalog items",
				resp.HTTPResponse,
				resp.Body,
			)
		}

		items = append(items, resp.JSON200.Items...)
		continueToken = resp.JSON200.Metadata.Continue
		if continueToken == nil || *continueToken == "" {
			return items, nil
		}
		if _, repeated := seenTokens[*continueToken]; repeated {
			return nil, fmt.Errorf(
				"server repeated pagination token while listing catalog items",
			)
		}
		seenTokens[*continueToken] = struct{}{}
	}
}

func managementSelector(pipelineID string) string {
	return fmt.Sprintf(
		"%s=%s,%s=%s",
		managedByLabel,
		managedByValue,
		pipelineLabel,
		pipelineID,
	)
}

func (c *reconciler) getCatalog(
	ctx context.Context,
	name string,
) (*apiv1alpha1.Catalog, error) {
	resp, err := c.client.GetCatalogWithResponse(ctx, name)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("GET request returned no response")
	}
	if responseStatusCode(resp.HTTPResponse) == http.StatusNotFound {
		return nil, nil
	}
	if resp.JSON200 != nil {
		return resp.JSON200, nil
	}
	return nil, apiError("GET", resp.HTTPResponse, resp.Body)
}

func (c *reconciler) replaceCatalog(
	ctx context.Context,
	name string,
	catalog apiv1alpha1.Catalog,
) error {
	clearCatalogServerFields(&catalog)
	resp, err := c.client.ReplaceCatalogWithResponse(ctx, name, catalog)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("PUT request returned no response")
	}
	if resp.JSON200 != nil || resp.JSON201 != nil {
		return nil
	}
	return apiError("PUT", resp.HTTPResponse, resp.Body)
}

func (c *reconciler) deleteCatalog(
	ctx context.Context,
	name string,
) error {
	resp, err := c.client.DeleteCatalogWithResponse(ctx, name)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("DELETE request returned no response")
	}
	status := responseStatusCode(resp.HTTPResponse)
	if resp.JSON200 != nil || status == http.StatusNotFound {
		return nil
	}
	return apiError("DELETE", resp.HTTPResponse, resp.Body)
}

func (c *reconciler) getCatalogItem(
	ctx context.Context,
	catalogName string,
	itemName string,
) (*apiv1alpha1.CatalogItem, error) {
	resp, err := c.client.GetCatalogItemWithResponse(ctx, catalogName, itemName)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("GET request returned no response")
	}
	if responseStatusCode(resp.HTTPResponse) == http.StatusNotFound {
		return nil, nil
	}
	if resp.JSON200 != nil {
		return resp.JSON200, nil
	}
	return nil, apiError("GET", resp.HTTPResponse, resp.Body)
}

func (c *reconciler) replaceCatalogItem(
	ctx context.Context,
	catalogName string,
	itemName string,
	item apiv1alpha1.CatalogItem,
) error {
	clearCatalogItemServerFields(&item)
	resp, err := c.client.ReplaceCatalogItemWithResponse(
		ctx,
		catalogName,
		itemName,
		item,
	)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("PUT request returned no response")
	}
	if resp.JSON200 != nil || resp.JSON201 != nil {
		return nil
	}
	return apiError("PUT", resp.HTTPResponse, resp.Body)
}

func (c *reconciler) deleteCatalogItem(
	ctx context.Context,
	catalogName string,
	itemName string,
) error {
	resp, err := c.client.DeleteCatalogItemWithResponse(
		ctx,
		catalogName,
		itemName,
	)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("DELETE request returned no response")
	}
	status := responseStatusCode(resp.HTTPResponse)
	if resp.JSON200 != nil || status == http.StatusNotFound {
		return nil
	}
	return apiError("DELETE", resp.HTTPResponse, resp.Body)
}

func clearCatalogServerFields(catalog *apiv1alpha1.Catalog) {
	catalog.Metadata.CreationTimestamp = nil
	catalog.Metadata.DeletionTimestamp = nil
	catalog.Metadata.Generation = nil
	catalog.Metadata.Owner = nil
	catalog.Metadata.Annotations = nil
	catalog.Metadata.ResourceVersion = nil
	catalog.Status = nil
}

func clearCatalogItemServerFields(item *apiv1alpha1.CatalogItem) {
	item.Metadata.CreationTimestamp = nil
	item.Metadata.DeletionTimestamp = nil
	item.Metadata.Generation = nil
	item.Metadata.Owner = nil
	item.Metadata.Annotations = nil
	item.Metadata.ResourceVersion = nil
}

func labelsEqual(left *map[string]string, right *map[string]string) bool {
	return maps.Equal(labelsValue(left), labelsValue(right))
}

func labelsValue(labels *map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	return *labels
}

func apiError(method string, resp *http.Response, body []byte) error {
	if resp == nil {
		return fmt.Errorf("%s request returned no HTTP response", method)
	}

	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return fmt.Errorf("unexpected %s response: %s", method, resp.Status)
	}

	truncated := len(body) > maxAPIErrorBodyBytes
	if truncated {
		body = body[:maxAPIErrorBodyBytes]
	}

	detail := strings.ToValidUTF8(string(body), "\uFFFD")
	if truncated {
		detail += "…"
	}

	// Quote the untrusted response so newlines and control characters cannot
	// inject misleading entries into logs.
	return fmt.Errorf(
		"unexpected %s response: %s: server response %s",
		method,
		resp.Status,
		strconv.Quote(detail),
	)
}

func responseStatusCode(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func lessCatalogItemKey(left catalogItemKey, right catalogItemKey) bool {
	if left.catalog != right.catalog {
		return left.catalog < right.catalog
	}
	return left.name < right.name
}

func nameOf(name *string) string {
	if name == nil {
		return ""
	}
	return *name
}
