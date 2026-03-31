package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCompleteConfig verifies that completeConfig properly handles various auth configurations,
// ensuring that explicitly provided client IDs are preserved and non-AAP setups remain unchanged.
func TestCompleteConfig(t *testing.T) {
	opts := &RenderTemplateOptions{}

	tests := []struct {
		name           string
		inputData      map[string]interface{}
		expectedConfig map[string]interface{}
	}{
		{
			name: "AAP auth with manual clientId should ignore file",
			inputData: map[string]interface{}{
				"global": map[string]interface{}{
					"baseDomain": "example.com",
					"auth": map[string]interface{}{
						"type": "aap",
						"aap": map[string]interface{}{
							"clientId": "manual-client-id",
						},
					},
				},
			},
			expectedConfig: map[string]interface{}{
				"global": map[string]interface{}{
					"baseDomain": "example.com",
					"auth": map[string]interface{}{
						"type": "aap",
						"aap": map[string]interface{}{
							"clientId": "manual-client-id",
						},
					},
				},
			},
		},
		{
			name: "Non-AAP auth should not inject clientId",
			inputData: map[string]interface{}{
				"global": map[string]interface{}{
					"baseDomain": "example.com",
					"auth": map[string]interface{}{
						"type": "oidc",
					},
				},
			},
			expectedConfig: map[string]interface{}{
				"global": map[string]interface{}{
					"baseDomain": "example.com",
					"auth": map[string]interface{}{
						"type": "oidc",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := opts.completeConfig(tt.inputData)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedConfig, tt.inputData)
		})
	}
}
