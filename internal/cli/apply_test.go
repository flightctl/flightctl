package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestApplyRepositoryDevicePullAccess(t *testing.T) {
	for _, flag := range []*bool{nil, lo.ToPtr(false), lo.ToPtr(true)} {
		state, err := json.Marshal(flag)
		require.NoError(t, err)
		t.Run("When delivery is "+string(state)+" it should round trip get YAML through apply", func(t *testing.T) {
			repo := repositoryWithDelivery(t, flag)
			input := getRepositoryYAML(t, repo)
			c, recorder := newRecordingClient(t, http.StatusOK, repo)
			require.Empty(t, applyFromReader(t.Context(), c, nil, "repository.yaml", strings.NewReader(input), false))
			require.Len(t, recorder.requests, 1)
			require.Equal(t, http.MethodPut, recorder.requests[0].Method)
			require.Equal(t, "/repositories/registry", recorder.requests[0].URL.Path)
			var result api.Repository
			require.NoError(t, json.Unmarshal([]byte(recorder.bodies[0]), &result))
			require.Equal(t, repo.Spec, result.Spec)
		})
	}
}

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
