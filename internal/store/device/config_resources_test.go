package device

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestConfigResourcesMutationClone(t *testing.T) {
	var resources []domain.ConfigResource
	require.NoError(t, json.Unmarshal([]byte(`[{"name":"registry:quay.io","type":"registry","registry":"quay.io","scheme":"https","skipServerVerification":false,"access":[{"scope":"","consumers":[{"type":"os"}]}]}]`), &resources))
	original := &DeviceMutation{Device: &domain.Device{Spec: &domain.DeviceSpec{}}, CurrentConfigResources: resources}
	copy, err := original.Clone()
	require.NoError(t, err)
	cloned := copy.(*DeviceMutation)
	require.Equal(t, resources, cloned.CurrentConfigResources)
	require.NoError(t, json.Unmarshal([]byte(`{"name":"registry:other.io","type":"registry"}`), &cloned.CurrentConfigResources[0]))
	require.NotEqual(t, original.CurrentConfigResources, cloned.CurrentConfigResources)
	require.Nil(t, cloned.Resource().Spec.ConfigResources)
}
