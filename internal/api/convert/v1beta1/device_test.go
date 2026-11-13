package v1beta1

import (
	"encoding/json"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/require"
)

func TestDeviceConfigResourcesConversion(t *testing.T) {
	input := `{"spec":{"configResources":[{"name":"registry:quay.io","type":"registry","registry":"quay.io","scheme":"https","skipServerVerification":false,"access":[{"scope":"","consumers":[{"type":"os"}]}]}]}}`
	var device api.Device
	require.NoError(t, json.Unmarshal([]byte(input), &device))
	converter := NewDeviceConverter()
	domain := converter.ToDomain(device)
	output, err := json.Marshal(converter.FromDomain(&domain))
	require.NoError(t, err)
	var restored api.Device
	require.NoError(t, json.Unmarshal(output, &restored))
	require.Equal(t, device.Spec, restored.Spec)
	resource, err := (*restored.Spec.ConfigResources)[0].AsRegistryConfigResource()
	require.NoError(t, err)
	require.NotNil(t, resource.SkipServerVerification)
	require.False(t, *resource.SkipServerVerification)
	require.Equal(t, "", *resource.Access[0].Scope)
}
