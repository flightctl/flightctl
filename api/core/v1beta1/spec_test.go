package v1beta1

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSpec(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	require.NotNil(t, spec.Paths)
	require.NotZero(t, spec.Paths.Len())
	require.NotNil(t, spec.Components)
	require.NotEmpty(t, spec.Components.Schemas)
}

func TestGetSpecIsCached(t *testing.T) {
	first, err := GetSpec()
	require.NoError(t, err)
	second, err := GetSpec()
	require.NoError(t, err)
	require.Same(t, first, second)
}

func TestRepositoryDevicePullAccessSchema(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	ociSchema := spec.Components.Schemas["OciRepoSpec"].Value
	require.Equal(t, false, ociSchema.Properties["devicePullAccess"].Value.Default)
	require.NotContains(t, ociSchema.Required, "devicePullAccess")
	for _, mode := range []string{"Read", "ReadWrite", ""} {
		for _, tc := range []struct {
			name    string
			value   any
			present bool
			valid   bool
		}{
			{name: "true", value: true, present: true, valid: true},
			{name: "false", value: false, present: true, valid: true},
			{name: "omitted", valid: true},
			{name: "string", value: "true", present: true},
			{name: "number", value: 1, present: true},
			{name: "object", value: map[string]any{}, present: true},
			{name: "null", present: true},
		} {
			t.Run(fmt.Sprintf("When accessMode is %s and delivery is %s it should enforce boolean validation", mode, tc.name), func(t *testing.T) {
				body := map[string]any{"type": "oci", "registry": "quay.io"}
				if mode != "" {
					body["accessMode"] = mode
				}
				if tc.present {
					body["devicePullAccess"] = tc.value
				}
				err := spec.Components.Schemas["OciRepoSpec"].Value.VisitJSON(body)
				if tc.valid {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
	for _, kind := range []string{"GitRepoSpec", "HttpRepoSpec"} {
		t.Run("When delivery is set on "+kind+" it should reject the OCI-only field", func(t *testing.T) {
			body := map[string]any{"type": "http", "url": "https://example.com", "devicePullAccess": true}
			if kind == "GitRepoSpec" {
				body["type"] = "git"
			}
			require.Error(t, spec.Components.Schemas[kind].Value.VisitJSON(body))
			delete(body, "devicePullAccess")
			require.NoError(t, spec.Components.Schemas[kind].Value.VisitJSON(body))
		})
	}
}

func TestSpecYAMLMatchesEmbeddedDocument(t *testing.T) {
	require.NotEmpty(t, SpecYAML())
	require.Equal(t, specYAML, SpecYAML())
}
