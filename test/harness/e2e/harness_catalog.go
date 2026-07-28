package e2e

import (
	"fmt"
	"net/http"

	v1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
)

// CreateCatalog creates a new catalog with the given name and display name.
func (h *Harness) CreateCatalog(name, displayName string) (*v1alpha1.Catalog, error) {
	client := h.GetV1Alpha1Client()
	if client == nil {
		return nil, fmt.Errorf("v1alpha1 client not available")
	}

	catalog := v1alpha1.Catalog{
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: v1alpha1.CatalogSpec{
			DisplayName: lo.ToPtr(displayName),
		},
	}

	resp, err := client.CreateCatalogWithResponse(h.Context, catalog)
	if err != nil {
		return nil, fmt.Errorf("failed to create catalog %s: %w", name, err)
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("failed to create catalog %s: unexpected status %d, body: %s",
			name, resp.StatusCode(), string(resp.Body))
	}
	return resp.JSON201, nil
}

// CreateCatalogItem creates a new catalog item within the specified catalog.
func (h *Harness) CreateCatalogItem(catalogName, itemName string, spec v1alpha1.CatalogItemSpec) (*v1alpha1.CatalogItem, error) {
	client := h.GetV1Alpha1Client()
	if client == nil {
		return nil, fmt.Errorf("v1alpha1 client not available")
	}

	item := v1alpha1.CatalogItem{
		Metadata: v1alpha1.CatalogItemMeta{
			Name: lo.ToPtr(itemName),
		},
		Spec: spec,
	}

	resp, err := client.CreateCatalogItemWithResponse(h.Context, catalogName, item)
	if err != nil {
		return nil, fmt.Errorf("failed to create catalog item %s/%s: %w", catalogName, itemName, err)
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("failed to create catalog item %s/%s: unexpected status %d, body: %s",
			catalogName, itemName, resp.StatusCode(), string(resp.Body))
	}
	return resp.JSON201, nil
}

// GetCatalogItem retrieves a catalog item by catalog and item name.
func (h *Harness) GetCatalogItem(catalogName, itemName string) (*v1alpha1.CatalogItem, error) {
	client := h.GetV1Alpha1Client()
	if client == nil {
		return nil, fmt.Errorf("v1alpha1 client not available")
	}

	resp, err := client.GetCatalogItemWithResponse(h.Context, catalogName, itemName)
	if err != nil {
		return nil, fmt.Errorf("failed to get catalog item %s/%s: %w", catalogName, itemName, err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("failed to get catalog item %s/%s: unexpected status %d, body: %s",
			catalogName, itemName, resp.StatusCode(), string(resp.Body))
	}
	return resp.JSON200, nil
}

// DeleteCatalog deletes a catalog by name.
func (h *Harness) DeleteCatalog(name string) error {
	client := h.GetV1Alpha1Client()
	if client == nil {
		return fmt.Errorf("v1alpha1 client not available")
	}

	resp, err := client.DeleteCatalogWithResponse(h.Context, name)
	if err != nil {
		return fmt.Errorf("failed to delete catalog %s: %w", name, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("failed to delete catalog %s: unexpected status %d, body: %s",
			name, resp.StatusCode(), string(resp.Body))
	}
	return nil
}

// DeleteCatalogIgnoreNotFound deletes a catalog, returning nil if not found.
func (h *Harness) DeleteCatalogIgnoreNotFound(name string) error {
	client := h.GetV1Alpha1Client()
	if client == nil {
		return fmt.Errorf("v1alpha1 client not available")
	}

	resp, err := client.DeleteCatalogWithResponse(h.Context, name)
	if err != nil {
		return fmt.Errorf("failed to delete catalog %s: %w", name, err)
	}
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusNotFound {
		return fmt.Errorf("failed to delete catalog %s: unexpected status %d, body: %s",
			name, resp.StatusCode(), string(resp.Body))
	}
	return nil
}

// DeleteCatalogItem deletes a catalog item by catalog and item name.
func (h *Harness) DeleteCatalogItem(catalogName, itemName string) error {
	client := h.GetV1Alpha1Client()
	if client == nil {
		return fmt.Errorf("v1alpha1 client not available")
	}

	resp, err := client.DeleteCatalogItemWithResponse(h.Context, catalogName, itemName)
	if err != nil {
		return fmt.Errorf("failed to delete catalog item %s/%s: %w", catalogName, itemName, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("failed to delete catalog item %s/%s: unexpected status %d, body: %s",
			catalogName, itemName, resp.StatusCode(), string(resp.Body))
	}
	return nil
}

// DeleteCatalogItemIgnoreNotFound deletes a catalog item, returning nil if not found.
func (h *Harness) DeleteCatalogItemIgnoreNotFound(catalogName, itemName string) error {
	client := h.GetV1Alpha1Client()
	if client == nil {
		return fmt.Errorf("v1alpha1 client not available")
	}

	resp, err := client.DeleteCatalogItemWithResponse(h.Context, catalogName, itemName)
	if err != nil {
		return fmt.Errorf("failed to delete catalog item %s/%s: %w", catalogName, itemName, err)
	}
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusNotFound {
		return fmt.Errorf("failed to delete catalog item %s/%s: unexpected status %d, body: %s",
			catalogName, itemName, resp.StatusCode(), string(resp.Body))
	}
	return nil
}

// CatalogVersionRef pairs a semver version identifier with the full image reference
// that the catalog resolver returns for that version.
type CatalogVersionRef struct {
	Version  string // semver version (e.g. "1.0.0")
	ImageRef string // resolved image reference (e.g. "registry:5000/org/image:v1")
}

// NewOSCatalogItemSpec builds a CatalogItemSpec for an OS-type catalog item with a single version.
// The artifact type must be "container" because resolveCatalogItemRef always looks up that type.
func NewOSCatalogItemSpec(imageURI string, vr CatalogVersionRef, channel string) v1alpha1.CatalogItemSpec {
	return v1alpha1.CatalogItemSpec{
		DisplayName: lo.ToPtr("Test OS Item"),
		Category:    lo.ToPtr(v1alpha1.CatalogItemCategorySystem),
		Type:        v1alpha1.CatalogItemTypeOS,
		Artifacts: []v1alpha1.CatalogItemArtifact{
			{Type: v1alpha1.CatalogItemArtifactTypeContainer, Uri: imageURI},
		},
		Versions: []v1alpha1.CatalogItemVersion{
			{
				Version:    vr.Version,
				References: map[v1alpha1.CatalogItemArtifactType]string{v1alpha1.CatalogItemArtifactTypeContainer: vr.ImageRef},
				Channels:   []string{channel},
			},
		},
	}
}

// NewAppCatalogItemSpec builds a CatalogItemSpec for an application-type catalog item with a single version.
func NewAppCatalogItemSpec(imageURI string, vr CatalogVersionRef, channel string) v1alpha1.CatalogItemSpec {
	return v1alpha1.CatalogItemSpec{
		DisplayName: lo.ToPtr("Test App Item"),
		Category:    lo.ToPtr(v1alpha1.CatalogItemCategoryApplication),
		Type:        v1alpha1.CatalogItemTypeContainer,
		Artifacts: []v1alpha1.CatalogItemArtifact{
			{Type: v1alpha1.CatalogItemArtifactTypeContainer, Uri: imageURI},
		},
		Versions: []v1alpha1.CatalogItemVersion{
			{
				Version:    vr.Version,
				References: map[v1alpha1.CatalogItemArtifactType]string{v1alpha1.CatalogItemArtifactTypeContainer: vr.ImageRef},
				Channels:   []string{channel},
			},
		},
	}
}

// NewOSCatalogItemSpecMultiVersion builds a CatalogItemSpec for an OS-type item with multiple versions.
func NewOSCatalogItemSpecMultiVersion(imageURI string, versions []CatalogVersionRef, channel string) v1alpha1.CatalogItemSpec {
	catalogVersions := make([]v1alpha1.CatalogItemVersion, 0, len(versions))
	for _, vr := range versions {
		catalogVersions = append(catalogVersions, v1alpha1.CatalogItemVersion{
			Version:    vr.Version,
			References: map[v1alpha1.CatalogItemArtifactType]string{v1alpha1.CatalogItemArtifactTypeContainer: vr.ImageRef},
			Channels:   []string{channel},
		})
	}
	return v1alpha1.CatalogItemSpec{
		DisplayName: lo.ToPtr("Test OS Item"),
		Category:    lo.ToPtr(v1alpha1.CatalogItemCategorySystem),
		Type:        v1alpha1.CatalogItemTypeOS,
		Artifacts: []v1alpha1.CatalogItemArtifact{
			{Type: v1alpha1.CatalogItemArtifactTypeContainer, Uri: imageURI},
		},
		Versions: catalogVersions,
	}
}
