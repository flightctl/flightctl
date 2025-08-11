package tasks

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestResourceSync_GetRepositoryAndValidateAccess_NilResourceSync(t *testing.T) {
	// Create a minimal ResourceSync instance with nil dependencies
	var serviceHandler service.Service
	log := logrus.New()

	resourceSync := NewResourceSync(serviceHandler, log, nil)

	// Test with nil ResourceSync
	repo, err := resourceSync.GetRepositoryAndValidateAccess(context.Background(), nil)

	assert.Error(t, err)
	assert.Nil(t, repo)
	assert.Contains(t, err.Error(), "ResourceSync is nil")
}

func TestResourceSync_ParseFleetsFromResources_ValidResources(t *testing.T) {
	// Create a minimal ResourceSync instance with nil dependencies
	var serviceHandler service.Service
	log := logrus.New()

	resourceSync := NewResourceSync(serviceHandler, log, nil)

	// Create valid resources
	resources := []GenericResourceMap{
		{
			"kind": api.FleetKind,
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

func TestResourceSync_ParseFleetsFromResources_InvalidResources(t *testing.T) {
	// Create a minimal ResourceSync instance with nil dependencies
	var serviceHandler service.Service
	log := logrus.New()

	resourceSync := NewResourceSync(serviceHandler, log, nil)

	// Create invalid resources
	resources := []GenericResourceMap{
		{
			"kind": "InvalidKind",
			"metadata": map[string]interface{}{
				"name": "test-fleet",
			},
		},
	}

	// Test parsing
	fleets, err := resourceSync.ParseFleetsFromResources(resources, "test-resourcesync")

	assert.Error(t, err)
	assert.Nil(t, fleets)
	assert.Contains(t, err.Error(), "resource of unknown/unsupported kind")
}

func TestRemoveIgnoredFields_NoIgnoredFields(t *testing.T) {
	// Test with no ignored fields
	resource := GenericResourceMap{
		"kind": api.FleetKind,
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
		"kind": api.FleetKind,
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
		"kind": api.FleetKind,
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
		"kind": api.FleetKind,
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
		"kind": api.FleetKind,
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
		"kind": api.FleetKind,
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
		"kind": api.FleetKind,
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
		"kind": api.FleetKind,
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
	resourceSync := NewResourceSync(serviceHandler, log, ignorePaths)

	// Create resources with fields that should be ignored
	resources := []GenericResourceMap{
		{
			"kind": api.FleetKind,
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
