package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplySingleResourceEmptyName(t *testing.T) {
	tests := []struct {
		name        string
		resource    genericResource
		wantErr     bool
		errContains string
	}{
		{
			name: "empty name returns error",
			resource: genericResource{
				"apiVersion": "flightctl.io/v1alpha1",
				"kind":       "Catalog",
				"metadata": map[string]interface{}{
					"name": "",
				},
			},
			wantErr:     true,
			errContains: "metadata.name must not be empty",
		},
		{
			name: "missing name key returns error",
			resource: genericResource{
				"apiVersion": "flightctl.io/v1alpha1",
				"kind":       "Catalog",
				"metadata":   map[string]interface{}{},
			},
			wantErr:     true,
			errContains: "unspecified resource name",
		},
		{
			name: "missing metadata returns error",
			resource: genericResource{
				"apiVersion": "flightctl.io/v1alpha1",
				"kind":       "Catalog",
			},
			wantErr:     true,
			errContains: "unspecified metadata",
		},
		{
			name: "valid name proceeds (dry run)",
			resource: genericResource{
				"apiVersion": "flightctl.io/v1alpha1",
				"kind":       "Catalog",
				"metadata": map[string]interface{}{
					"name": "my-catalog",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			errs := applySingleResource(t.Context(), nil, nil, "test.yaml", tt.resource, true)
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}
