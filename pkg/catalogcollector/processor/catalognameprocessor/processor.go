package catalognameprocessor

import (
	"context"
	"fmt"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
)

type processor struct {
	id       catalogcollector.ComponentID
	log      *logrus.Entry
	mappings map[string]string
	next     catalogcollector.Consumer
}

func (p *processor) Consume(
	ctx context.Context,
	snapshot *catalogcollector.CatalogSnapshot,
) error {
	p.log.WithFields(logrus.Fields{
		"revision":           snapshot.Revision,
		"catalog_count":      len(snapshot.Catalogs),
		"catalog_item_count": len(snapshot.CatalogItems),
		"mapping_count":      len(p.mappings),
	}).Debug("applying catalog name mappings")

	out, err := p.transform(snapshot)
	if err != nil {
		p.log.WithError(err).Error("transformation failed")
		return fmt.Errorf("processor %q: %w", p.id, err)
	}

	return p.next.Consume(ctx, out)
}

func (p *processor) transform(
	snapshot *catalogcollector.CatalogSnapshot,
) (*catalogcollector.CatalogSnapshot, error) {
	catalogs, err := p.transformCatalogs(snapshot.Catalogs)
	if err != nil {
		return nil, err
	}

	items, err := p.transformCatalogItems(snapshot.CatalogItems)
	if err != nil {
		return nil, err
	}

	return &catalogcollector.CatalogSnapshot{
		Revision:     snapshot.Revision,
		Catalogs:     catalogs,
		CatalogItems: items,
	}, nil
}

func (p *processor) transformCatalogs(
	in []apiv1alpha1.Catalog,
) ([]apiv1alpha1.Catalog, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]apiv1alpha1.Catalog, len(in))

	for i, catalog := range in {
		name := nameOf(catalog.Metadata.Name)
		renamed := name

		if mapped, ok := p.mappings[name]; ok {
			renamed = mapped
		}

		if _, duplicate := seen[renamed]; duplicate {
			return nil, fmt.Errorf(
				"transformation would create duplicate Catalog %q",
				renamed,
			)
		}
		seen[renamed] = struct{}{}

		transformed := catalog
		if renamed != name {
			transformed.Metadata = shallowCopyObjectMeta(catalog.Metadata)
			transformed.Metadata.Name = ptrTo(renamed)
		}

		out[i] = transformed
	}

	return out, nil
}

func (p *processor) transformCatalogItems(
	in []apiv1alpha1.CatalogItem,
) ([]apiv1alpha1.CatalogItem, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]apiv1alpha1.CatalogItem, len(in))

	for i, item := range in {
		catalogName := item.Metadata.Catalog
		if mapped, ok := p.mappings[catalogName]; ok {
			catalogName = mapped
		}

		itemName := nameOf(item.Metadata.Name)
		key := catalogName + "\x00" + itemName

		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf(
				"transformation would create duplicate CatalogItem %q/%q",
				catalogName,
				itemName,
			)
		}
		seen[key] = struct{}{}

		transformed := item
		if catalogName != item.Metadata.Catalog {
			transformed.Metadata = shallowCopyCatalogItemMeta(item.Metadata)
			transformed.Metadata.Catalog = catalogName
		}

		out[i] = transformed
	}

	return out, nil
}

func nameOf(name *string) string {
	if name == nil {
		return ""
	}
	return *name
}

func ptrTo(value string) *string {
	return &value
}
