package v1beta1

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthoredConfigResources(t *testing.T) {
	for _, fleet := range []bool{false, true} {
		t.Run("When an authored collection is present it should reject even when empty", func(t *testing.T) {
			spec := DeviceSpec{ConfigResources: &[]ConfigResource{}}
			require.ErrorContains(t, errors.Join(spec.Validate(fleet)...), "configResources")
			require.Empty(t, (DeviceSpec{}).Validate(fleet))
		})
		t.Run("When application pullConfig is authored it should reject", func(t *testing.T) {
			var app ApplicationProviderSpec
			require.NoError(t, json.Unmarshal([]byte(`{"appType":"container","name":"app","image":"quay.io/example/app:v1","pullConfig":null}`), &app))
			spec := DeviceSpec{Applications: &[]ApplicationProviderSpec{app}}
			require.ErrorContains(t, errors.Join(spec.Validate(fleet)...), "pullConfig")
		})
	}
}

func TestPullConfigSchemaRejection(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	for _, schemaName := range []string{"DeviceOsSpec", "ContainerApplication", "ComposeApplication", "QuadletApplication", "HelmApplication", "VmApplication"} {
		t.Run("When "+schemaName+" contains pullConfig it should reject", func(t *testing.T) {
			payload := map[string]any{"image": "quay.io/example/app:v1"}
			if schemaName != "DeviceOsSpec" {
				kinds := map[string]string{"ContainerApplication": "container", "ComposeApplication": "compose", "QuadletApplication": "quadlet", "HelmApplication": "helm", "VmApplication": "vm"}
				payload["appType"] = kinds[schemaName]
			}
			schema := spec.Components.Schemas[schemaName].Value
			require.NoError(t, schema.VisitJSON(payload))
			for _, value := range []any{nil, map[string]any{}, "secret-sentinel"} {
				payload["pullConfig"] = value
				require.Error(t, schema.VisitJSON(payload))
			}
		})
	}
}
