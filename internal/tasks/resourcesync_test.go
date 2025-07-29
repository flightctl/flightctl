package tasks

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestResourceSync_GetRepositoryAndValidateAccess_NilResourceSync(t *testing.T) {
	// Create a minimal ResourceSync instance with nil dependencies
	var callbackManager tasks_client.CallbackManager
	var serviceHandler service.Service
	log := logrus.New()

	resourceSync := NewResourceSync(callbackManager, serviceHandler, log)

	// Test with nil ResourceSync
	repo, err := resourceSync.GetRepositoryAndValidateAccess(context.Background(), nil)

	assert.Error(t, err)
	assert.Nil(t, repo)
	assert.Contains(t, err.Error(), "ResourceSync is nil")
}

func TestResourceSync_ParseFleetsFromResources_ValidResources(t *testing.T) {
	// Create a minimal ResourceSync instance with nil dependencies
	var callbackManager tasks_client.CallbackManager
	var serviceHandler service.Service
	log := logrus.New()

	resourceSync := NewResourceSync(callbackManager, serviceHandler, log)

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
	var callbackManager tasks_client.CallbackManager
	var serviceHandler service.Service
	log := logrus.New()

	resourceSync := NewResourceSync(callbackManager, serviceHandler, log)

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
