package catalogcollector_test

import (
	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
)

const (
	catalogAPIVersion = "v1alpha1"
	catalogKind       = "Catalog"
	catalogItemKind   = "CatalogItem"
)

// newCatalog returns a minimal valid Catalog for snapshot and direct-API use.
func newCatalog(name, displayName string) apiv1alpha1.Catalog {
	return apiv1alpha1.Catalog{
		ApiVersion: catalogAPIVersion,
		Kind:       catalogKind,
		Metadata: apiv1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: apiv1alpha1.CatalogSpec{
			DisplayName: lo.ToPtr(displayName),
		},
	}
}

// newCatalogItem returns a minimal valid CatalogItem attached to catalog.
func newCatalogItem(catalog, name, displayName string) apiv1alpha1.CatalogItem {
	return apiv1alpha1.CatalogItem{
		ApiVersion: catalogAPIVersion,
		Kind:       catalogItemKind,
		Metadata: apiv1alpha1.CatalogItemMeta{
			Catalog: catalog,
			Name:    lo.ToPtr(name),
		},
		Spec: apiv1alpha1.CatalogItemSpec{
			DisplayName: lo.ToPtr(displayName),
			Category:    lo.ToPtr(apiv1alpha1.CatalogItemCategoryApplication),
			Type:        apiv1alpha1.CatalogItemTypeContainer,
			Artifacts: []apiv1alpha1.CatalogItemArtifact{
				{
					Type: apiv1alpha1.CatalogItemArtifactTypeContainer,
					Uri:  "quay.io/flightctl-tests/" + name,
				},
			},
			Versions: []apiv1alpha1.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[apiv1alpha1.CatalogItemArtifactType]string{"container": "v1.0.0"},
					Channels:   []string{"stable"},
				},
			},
		},
	}
}

// withCatalogLabels returns a copy of catalog carrying the supplied labels.
func withCatalogLabels(catalog apiv1alpha1.Catalog, labels map[string]string) apiv1alpha1.Catalog {
	catalog.Metadata.Labels = lo.ToPtr(labels)
	return catalog
}

// labelsOf flattens the label pointer of a resource for comparison.
func labelsOf(labels *map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{}
	}
	return *labels
}
