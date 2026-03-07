package tasks

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestResourceSync_GetRepositoryAndValidateAccess_NilResourceSync(t *testing.T) {
	// Create a minimal ResourceSync instance with nil dependencies
	var serviceHandler service.Service
	log := logrus.New()

	resourceSync := NewResourceSync(serviceHandler, log, nil, nil)

	// Test with nil ResourceSync
	testOrgId := uuid.New()
	repo, err := resourceSync.GetRepositoryAndValidateAccess(context.Background(), testOrgId, nil)

	assert.Error(t, err)
	assert.Nil(t, repo)
	assert.Contains(t, err.Error(), "ResourceSync is nil")
}

func TestResourceSync_ParseFleetsFromResources_ValidResources(t *testing.T) {
	// Create a minimal ResourceSync instance with nil dependencies
	var serviceHandler service.Service
	log := logrus.New()

	resourceSync := NewResourceSync(serviceHandler, log, nil, nil)

	// Create valid resources
	resources := []GenericResourceMap{
		{
			"kind": domain.FleetKind,
			"metadata": map[string]interface{}{
				"name": "test-fleet",
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"environment": "test",
						},
					},
					"spec": map[string]interface{}{
						"os": map[string]interface{}{
							"image": "quay.io/test/os:latest",
						},
					},
				},
			},
		},
	}

	// Test parsing
	fleets, err := resourceSync.ParseFleetsFromResources(resources, "test-resourcesync")

	assert.NoError(t, err)
	assert.NotNil(t, fleets)
	assert.Len(t, fleets, 1)
	assert.Equal(t, "test-fleet", *fleets[0].Metadata.Name)
}

func TestResourceSync_ParseFleetsFromResources_SkipsNonFleetKinds(t *testing.T) {
	// Create a minimal ResourceSync instance with nil dependencies
	var serviceHandler service.Service
	log := logrus.New()

	resourceSync := NewResourceSync(serviceHandler, log, nil, nil)

	// Create resources with non-Fleet kinds -- these should be silently skipped
	resources := []GenericResourceMap{
		{
			"kind": "InvalidKind",
			"metadata": map[string]interface{}{
				"name": "test-fleet",
			},
		},
	}

	// Test parsing -- non-Fleet kinds are skipped, not errored
	fleets, err := resourceSync.ParseFleetsFromResources(resources, "test-resourcesync")

	assert.NoError(t, err)
	assert.Empty(t, fleets)
}

func TestRemoveIgnoredFields_NoIgnoredFields(t *testing.T) {
	// Test with no ignored fields
	resource := GenericResourceMap{
		"kind": domain.FleetKind,
		"metadata": map[string]interface{}{
			"name": "test-fleet",
			"labels": map[string]interface{}{
				"environment": "test",
			},
		},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"app": "test-app",
					},
				},
			},
		},
	}

	result := RemoveIgnoredFields(resource, nil)

	// Should return the same resource unchanged
	assert.Equal(t, resource, result)
	assert.Equal(t, "test-fleet", result["metadata"].(map[string]interface{})["name"])
	assert.Equal(t, "test", result["metadata"].(map[string]interface{})["labels"].(map[string]interface{})["environment"])
}

func TestRemoveIgnoredFields_RemoveTopLevelField(t *testing.T) {
	// Test removing a top-level field
	resource := GenericResourceMap{
		"kind": domain.FleetKind,
		"metadata": map[string]interface{}{
			"name": "test-fleet",
		},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"app": "test-app",
					},
				},
			},
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type": "Ready",
				},
			},
		},
	}

	ignorePaths := []string{"status"}
	result := RemoveIgnoredFields(resource, ignorePaths)

	// Should remove the status field from the result
	assert.NotContains(t, result, "status")
	assert.Contains(t, result, "kind")
	assert.Contains(t, result, "metadata")
	assert.Contains(t, result, "spec")

	// The original resource should also be modified since the function modifies in place
	assert.NotContains(t, resource, "status")
}

func TestRemoveIgnoredFields_RemoveNestedField(t *testing.T) {
	// Test removing a nested field
	resource := GenericResourceMap{
		"kind": domain.FleetKind,
		"metadata": map[string]interface{}{
			"name": "test-fleet",
			"labels": map[string]interface{}{
				"environment": "test",
				"app":         "test-app",
			},
		},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"app": "test-app",
					},
				},
			},
		},
	}

	ignorePaths := []string{"metadata/labels/environment"}
	result := RemoveIgnoredFields(resource, ignorePaths)

	// Should remove the nested environment label from the result
	metadata := result["metadata"].(map[string]interface{})
	labels := metadata["labels"].(map[string]interface{})
	assert.NotContains(t, labels, "environment")
	assert.Contains(t, labels, "app")

	// The original resource should also be modified
	originalMetadata := resource["metadata"].(map[string]interface{})
	originalLabels := originalMetadata["labels"].(map[string]interface{})
	assert.NotContains(t, originalLabels, "environment")
}

func TestRemoveIgnoredFields_RemoveMultipleFields(t *testing.T) {
	// Test removing multiple fields
	resource := GenericResourceMap{
		"kind": domain.FleetKind,
		"metadata": map[string]interface{}{
			"name": "test-fleet",
			"labels": map[string]interface{}{
				"environment": "test",
			},
		},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"app": "test-app",
					},
				},
			},
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type": "Ready",
				},
			},
		},
	}

	ignorePaths := []string{"status", "metadata/labels/environment"}
	result := RemoveIgnoredFields(resource, ignorePaths)

	// Should remove both fields from the result
	assert.NotContains(t, result, "status")
	metadata := result["metadata"].(map[string]interface{})
	labels := metadata["labels"].(map[string]interface{})
	assert.NotContains(t, labels, "environment")

	// The original resource should also be modified
	assert.NotContains(t, resource, "status")
	originalMetadata := resource["metadata"].(map[string]interface{})
	originalLabels := originalMetadata["labels"].(map[string]interface{})
	assert.NotContains(t, originalLabels, "environment")
}

func TestRemoveIgnoredFields_RemoveDeeplyNestedField(t *testing.T) {
	// Test removing a deeply nested field
	resource := GenericResourceMap{
		"kind": domain.FleetKind,
		"metadata": map[string]interface{}{
			"name": "test-fleet",
		},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"app": "test-app",
					},
				},
				"spec": map[string]interface{}{
					"os": map[string]interface{}{
						"image": "quay.io/test/os:latest",
					},
				},
			},
		},
	}

	ignorePaths := []string{"spec/template/spec/os/image"}
	result := RemoveIgnoredFields(resource, ignorePaths)

	// Should remove the deeply nested image field from the result
	spec := result["spec"].(map[string]interface{})
	template := spec["template"].(map[string]interface{})
	templateSpec := template["spec"].(map[string]interface{})
	os := templateSpec["os"].(map[string]interface{})
	assert.NotContains(t, os, "image")

	// The original resource should also be modified
	originalSpec := resource["spec"].(map[string]interface{})
	originalTemplate := originalSpec["template"].(map[string]interface{})
	originalTemplateSpec := originalTemplate["spec"].(map[string]interface{})
	originalOs := originalTemplateSpec["os"].(map[string]interface{})
	assert.NotContains(t, originalOs, "image")
}

func TestRemoveIgnoredFields_NonExistentField(t *testing.T) {
	// Test removing a field that doesn't exist
	resource := GenericResourceMap{
		"kind": domain.FleetKind,
		"metadata": map[string]interface{}{
			"name": "test-fleet",
		},
	}

	ignorePaths := []string{"non/existent/field"}
	result := RemoveIgnoredFields(resource, ignorePaths)

	// Should return the same resource unchanged
	assert.Equal(t, resource, result)
}

func TestRemoveIgnoredFields_EmptyIgnorePaths(t *testing.T) {
	// Test with empty ignore paths
	resource := GenericResourceMap{
		"kind": domain.FleetKind,
		"metadata": map[string]interface{}{
			"name": "test-fleet",
		},
	}

	ignorePaths := []string{}
	result := RemoveIgnoredFields(resource, ignorePaths)

	// Should return the same resource unchanged
	assert.Equal(t, resource, result)
}

func TestRemoveIgnoredFields_WithLeadingSlash(t *testing.T) {
	// Test with ignore paths that have leading slashes
	resource := GenericResourceMap{
		"kind": domain.FleetKind,
		"metadata": map[string]interface{}{
			"name": "test-fleet",
			"labels": map[string]interface{}{
				"environment": "test",
			},
		},
	}

	ignorePaths := []string{"/metadata/labels/environment"}
	result := RemoveIgnoredFields(resource, ignorePaths)

	// Should remove the field (leading slash should be handled) from the result
	metadata := result["metadata"].(map[string]interface{})
	labels := metadata["labels"].(map[string]interface{})
	assert.NotContains(t, labels, "environment")

	// The original resource should also be modified
	originalMetadata := resource["metadata"].(map[string]interface{})
	originalLabels := originalMetadata["labels"].(map[string]interface{})
	assert.NotContains(t, originalLabels, "environment")
}

func TestResourceSync_WithIgnoredFields(t *testing.T) {
	// Test ResourceSync with ignored fields configuration
	var serviceHandler service.Service
	log := logrus.New()

	ignorePaths := []string{"metadata/labels/environment", "status"}
	resourceSync := NewResourceSync(serviceHandler, log, nil, ignorePaths)

	// Create resources with fields that should be ignored
	resources := []GenericResourceMap{
		{
			"kind": domain.FleetKind,
			"metadata": map[string]interface{}{
				"name": "test-fleet",
				"labels": map[string]interface{}{
					"environment": "test", // This should be removed
					"app":         "test-app",
				},
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"app": "test-app",
						},
					},
				},
			},
			"status": map[string]interface{}{ // This should be removed
				"conditions": []interface{}{
					map[string]interface{}{
						"type": "Ready",
					},
				},
			},
		},
	}

	// Test parsing - the ignored fields should be removed during processing
	fleets, err := resourceSync.ParseFleetsFromResources(resources, "test-resourcesync")

	assert.NoError(t, err)
	assert.NotNil(t, fleets)
	assert.Len(t, fleets, 1)
	assert.Equal(t, "test-fleet", *fleets[0].Metadata.Name)

	// The ignored fields should not be present in the resulting fleet
	// Note: We can't directly test this since ParseFleetsFromResources doesn't return the raw GenericResourceMap
	// but the field removal happens in extractResourcesFromFile which is called by parseAndValidateResources
}

func TestFilterByKind(t *testing.T) {
	resources := []GenericResourceMap{
		{"kind": domain.FleetKind, "metadata": map[string]interface{}{"name": "f1"}},
		{"kind": domain.CatalogKind, "metadata": map[string]interface{}{"name": "c1"}},
		{"kind": domain.CatalogItemKind, "metadata": map[string]interface{}{"name": "i1"}},
		{"kind": domain.FleetKind, "metadata": map[string]interface{}{"name": "f2"}},
		{"kind": domain.CatalogItemKind, "metadata": map[string]interface{}{"name": "i2"}},
	}

	fleets := filterByKind(resources, domain.FleetKind)
	assert.Len(t, fleets, 2)

	catalogs := filterByKind(resources, domain.CatalogKind)
	assert.Len(t, catalogs, 1)

	items := filterByKind(resources, domain.CatalogItemKind)
	assert.Len(t, items, 2)

	empty := filterByKind(resources, "NonExistent")
	assert.Len(t, empty, 0)
}

func TestParseCatalogs_Valid(t *testing.T) {
	var serviceHandler service.Service
	log := logrus.New()
	rs := NewResourceSync(serviceHandler, log, nil, nil)

	resources := []GenericResourceMap{
		{
			"apiVersion": "v1alpha1",
			"kind":       domain.CatalogKind,
			"metadata": map[string]interface{}{
				"name": "platform-apps",
			},
			"spec": map[string]interface{}{},
		},
	}

	catalogs, err := rs.parseCatalogs(resources)
	assert.NoError(t, err)
	assert.Len(t, catalogs, 1)
	assert.Equal(t, "platform-apps", *catalogs[0].Metadata.Name)
}

func TestParseCatalogs_DuplicateNames(t *testing.T) {
	var serviceHandler service.Service
	log := logrus.New()
	rs := NewResourceSync(serviceHandler, log, nil, nil)

	resources := []GenericResourceMap{
		{
			"apiVersion": "v1alpha1",
			"kind":       domain.CatalogKind,
			"metadata":   map[string]interface{}{"name": "dupe"},
			"spec":       map[string]interface{}{},
		},
		{
			"apiVersion": "v1alpha1",
			"kind":       domain.CatalogKind,
			"metadata":   map[string]interface{}{"name": "dupe"},
			"spec":       map[string]interface{}{},
		},
	}

	_, err := rs.parseCatalogs(resources)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "multiple catalog definitions")
}

func TestParseCatalogs_SkipsNonCatalogKinds(t *testing.T) {
	var serviceHandler service.Service
	log := logrus.New()
	rs := NewResourceSync(serviceHandler, log, nil, nil)

	resources := []GenericResourceMap{
		{
			"kind":     domain.FleetKind,
			"metadata": map[string]interface{}{"name": "a-fleet"},
		},
	}

	catalogs, err := rs.parseCatalogs(resources)
	assert.NoError(t, err)
	assert.Empty(t, catalogs)
}

func TestParseCatalogItems_Valid(t *testing.T) {
	var serviceHandler service.Service
	log := logrus.New()
	rs := NewResourceSync(serviceHandler, log, nil, nil)

	resources := []GenericResourceMap{
		{
			"apiVersion": "v1alpha1",
			"kind":       domain.CatalogItemKind,
			"metadata": map[string]interface{}{
				"name":    "prometheus",
				"catalog": "platform-apps",
			},
			"spec": map[string]interface{}{
				"type": "quadlet",
				"reference": map[string]interface{}{
					"uri": "quay.io/prometheus/node-exporter",
				},
				"versions": []interface{}{
					map[string]interface{}{
						"version":  "1.8.2",
						"tag":      "v1.8.2",
						"channels": []interface{}{"stable"},
					},
				},
			},
		},
	}

	items, err := rs.parseCatalogItems(resources)
	assert.NoError(t, err)
	assert.Len(t, items, 1)
	assert.Equal(t, "prometheus", *items[0].Metadata.Name)
	assert.Equal(t, "platform-apps", items[0].Metadata.Catalog)
}

func TestParseCatalogItems_MissingCatalog(t *testing.T) {
	var serviceHandler service.Service
	log := logrus.New()
	rs := NewResourceSync(serviceHandler, log, nil, nil)

	resources := []GenericResourceMap{
		{
			"apiVersion": "v1alpha1",
			"kind":       domain.CatalogItemKind,
			"metadata": map[string]interface{}{
				"name": "prometheus",
				// no catalog field
			},
			"spec": catalogItemSpecFixture(),
		},
	}

	_, err := rs.parseCatalogItems(resources)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing field .metadata.catalog")
}

func TestParseCatalogItems_DuplicateKey(t *testing.T) {
	var serviceHandler service.Service
	log := logrus.New()
	rs := NewResourceSync(serviceHandler, log, nil, nil)

	resources := []GenericResourceMap{
		{
			"apiVersion": "v1alpha1",
			"kind":       domain.CatalogItemKind,
			"metadata":   map[string]interface{}{"name": "prom", "catalog": "apps"},
			"spec":       catalogItemSpecFixture(),
		},
		{
			"apiVersion": "v1alpha1",
			"kind":       domain.CatalogItemKind,
			"metadata":   map[string]interface{}{"name": "prom", "catalog": "apps"},
			"spec":       catalogItemSpecFixture(),
		},
	}

	_, err := rs.parseCatalogItems(resources)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "multiple catalog item definitions")
}

func TestParseCatalogItems_SameNameDifferentCatalog(t *testing.T) {
	var serviceHandler service.Service
	log := logrus.New()
	rs := NewResourceSync(serviceHandler, log, nil, nil)

	resources := []GenericResourceMap{
		{
			"apiVersion": "v1alpha1",
			"kind":       domain.CatalogItemKind,
			"metadata":   map[string]interface{}{"name": "nginx", "catalog": "catalog-a"},
			"spec":       catalogItemSpecFixture(),
		},
		{
			"apiVersion": "v1alpha1",
			"kind":       domain.CatalogItemKind,
			"metadata":   map[string]interface{}{"name": "nginx", "catalog": "catalog-b"},
			"spec":       catalogItemSpecFixture(),
		},
	}

	items, err := rs.parseCatalogItems(resources)
	assert.NoError(t, err)
	assert.Len(t, items, 2)
}

func TestCatalogsDelta(t *testing.T) {
	owned := []domain.Catalog{
		{Metadata: domain.ObjectMeta{Name: strPtr("keep")}},
		{Metadata: domain.ObjectMeta{Name: strPtr("remove")}},
	}
	newOwned := []*domain.Catalog{
		{Metadata: domain.ObjectMeta{Name: strPtr("keep")}},
		{Metadata: domain.ObjectMeta{Name: strPtr("add")}},
	}

	delta := catalogsDelta(owned, newOwned)
	assert.Equal(t, []string{"remove"}, delta)
}

func TestCatalogItemsDelta(t *testing.T) {
	owned := []domain.CatalogItem{
		{Metadata: domain.CatalogItemMeta{Name: strPtr("keep"), Catalog: "apps"}},
		{Metadata: domain.CatalogItemMeta{Name: strPtr("remove"), Catalog: "apps"}},
	}
	newOwned := []*domain.CatalogItem{
		{Metadata: domain.CatalogItemMeta{Name: strPtr("keep"), Catalog: "apps"}},
		{Metadata: domain.CatalogItemMeta{Name: strPtr("add"), Catalog: "apps"}},
	}

	delta := catalogItemsDelta(owned, newOwned)
	assert.Equal(t, []string{"apps/remove"}, delta)
}

func catalogItemSpecFixture() map[string]interface{} {
	return map[string]interface{}{
		"type": "quadlet",
		"reference": map[string]interface{}{
			"uri": "quay.io/test/app",
		},
		"versions": []interface{}{
			map[string]interface{}{
				"version":  "1.0.0",
				"tag":      "v1.0.0",
				"channels": []interface{}{"stable"},
			},
		},
	}
}

func strPtr(s string) *string {
	return &s
}

func TestIsValidFile(t *testing.T) {
	// Test the isValidFile function
	testCases := []struct {
		filename string
		expected bool
	}{
		{"fleet.yaml", true},
		{"fleet.yml", true},
		{"fleet.json", true},
		{"fleet.txt", false},
		{"fleet", false},
		{"fleet.yaml.bak", false},
		{"fleet.YAML", false}, // Case sensitive
		{"fleet.YML", false},  // Case sensitive
		{"fleet.JSON", false}, // Case sensitive
	}

	for _, tc := range testCases {
		t.Run(tc.filename, func(t *testing.T) {
			result := isValidFile(tc.filename)
			assert.Equal(t, tc.expected, result, "Filename: %s", tc.filename)
		})
	}
}
