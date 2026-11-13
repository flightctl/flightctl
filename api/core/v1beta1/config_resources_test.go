package v1beta1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

const registryConfigJSON = `{"name":"registry:quay.io:5000","type":"registry","registry":"quay.io:5000","scheme":"https","skipServerVerification":false,"access":[{"scope":"","consumers":[{"type":"container","user":"workload"},{"type":"os"}]}]}`

func TestConfigResourcesRoundTrip(t *testing.T) {
	t.Run("When false and empty scope are explicit it should retain them", func(t *testing.T) {
		var resource ConfigResource
		require.NoError(t, json.Unmarshal([]byte(registryConfigJSON), &resource))
		registry, err := resource.AsRegistryConfigResource()
		require.NoError(t, err)
		require.NotNil(t, registry.SkipServerVerification)
		require.False(t, *registry.SkipServerVerification)
		require.NotNil(t, registry.Access[0].Scope)
		require.Empty(t, *registry.Access[0].Scope)
		var copied ConfigResource
		require.NoError(t, copied.FromRegistryConfigResource(registry))
		data, err := json.Marshal(copied)
		require.NoError(t, err)
		require.JSONEq(t, registryConfigJSON, string(data))
		spec := DeviceSpec{ConfigResources: &[]ConfigResource{copied}}
		data, err = json.Marshal(spec)
		require.NoError(t, err)
		var restored DeviceSpec
		require.NoError(t, json.Unmarshal(data, &restored))
		require.Equal(t, spec, restored)
	})
	t.Run("When the collection is omitted or empty it should decode", func(t *testing.T) {
		for _, input := range []string{`{}`, `{"configResources":[]}`} {
			var spec DeviceSpec
			require.NoError(t, json.Unmarshal([]byte(input), &spec))
			data, err := json.Marshal(spec)
			require.NoError(t, err)
			require.JSONEq(t, input, string(data))
		}
	})
}

func TestConfigResourceSchema(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	schema := spec.Components.Schemas["ConfigResource"]
	require.NotNil(t, schema)
	var valid map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(registryConfigJSON), &valid))
	require.NoError(t, schema.Value.VisitJSON(valid))
	for _, key := range []string{"name", "type", "registry", "scheme", "skipServerVerification", "access"} {
		t.Run("When "+key+" is missing it should reject", func(t *testing.T) {
			var candidate map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(registryConfigJSON), &candidate))
			delete(candidate, key)
			require.Error(t, schema.Value.VisitJSON(candidate))
		})
	}
}
